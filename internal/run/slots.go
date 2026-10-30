package run

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"obor/internal/config"
	"obor/internal/project"
)

// Slots are the machine-wide check slots (design "Machine-wide check slots (v2.4)"), the protocol of axis-alies
// scripts/checkSlots.ts: files slot-0 … slot-<Max-1> and merge-running-<project> in Dir, created exclusively, content
// "<pid> <epoch ms>\n"; the holder is the first field; a file is live while its holder is (0 counts as alive: a file
// just created; anything not a number does not). Release removes a file only while it still holds our pid.
//
//	worker: 1. our project's merge marker live and younger than BlockMax → wait "waiting: a merge check is running"
//	        2. live slots + live merge markers of every project (the legacy merge-running too) ≥ Max → wait
//	        3. claim the first free slot-<i>
//	merge:  claim our merge marker; a live one → wait "waiting for the other merge check". Never waits on worker slots.
//
// A file whose holder is dead is reclaimed under the per-path guard dir <path>.reclaim (stale after 10 s). Held
// (CHECK_SLOT_HELD, T-41): the parent already holds that file for this check; while its holder lives it is reused,
// nothing claimed.
type Slots struct {
	Dir      string
	Max      int
	BlockMax time.Duration
	Marker   string // merge-running-<project>
	Held     string
	Poll     time.Duration // between attempts (Acquire; default 1 s)
	Report   time.Duration // between wait lines (Acquire; default 30 s)
}

// SlotsFor reads the slot settings: env CHECK_SLOT_DIR / CHECK_SLOTS / CHECK_MERGE_BLOCK_MAX / CHECK_PROJECT, else
// slots.dir / slots.max / slots.merge_block_max / name, else /tmp/obor-check-slots, 2, 900 s, "default" (a project
// name with / or white space is "default" too).
func SlotsFor(cfg *config.Config, env project.Env) Slots {
	var cs config.Slots
	if cfg.Data.Slots != nil {
		cs = *cfg.Data.Slots
	}
	num := func(n *int) string {
		if n == nil {
			return ""
		}
		return strconv.Itoa(*n)
	}
	setting := func(envKey, v string) string {
		if e := env.Get(envKey); e != "" {
			return e
		}
		return v
	}
	count := func(s string, def int) int {
		if n, err := strconv.Atoi(s); err == nil && n >= 0 && !strings.HasPrefix(s, "+") {
			return n
		}
		return def
	}
	s := Slots{Dir: setting("CHECK_SLOT_DIR", cs.Dir), Max: count(setting("CHECK_SLOTS", num(cs.Max)), 2),
		BlockMax: time.Duration(count(setting("CHECK_MERGE_BLOCK_MAX", num(cs.MergeBlockMax)), 900)) * time.Second,
		Held:     env.Get("CHECK_SLOT_HELD")}
	if s.Dir == "" {
		s.Dir = "/tmp/obor-check-slots"
	}
	name := setting("CHECK_PROJECT", cfg.Data.Name)
	if name == "" || strings.ContainsAny(name, "/ \t\n\r\v\f") {
		name = "default"
	}
	s.Marker = "merge-running-" + name
	return s
}

// ErrNoDir: the slot dir cannot be created.
var ErrNoDir = errors.New("cannot create the check slot dir")

// holder is a slot file's holder field: a pid, "0" for an empty one, "NaN" when not a number; ok false: no file.
func holder(path string) (string, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	line, _, _ := strings.Cut(string(b), "\n")
	field, _, _ := strings.Cut(line, " ")
	switch {
	case field == "":
		return "0", true
	case strings.Trim(field, "0123456789") != "":
		return "NaN", true
	}
	return field, true
}

// alive: holder 0, or a process kill(0) reaches (or may not signal: another user's).
func alive(h string) bool {
	pid, err := strconv.Atoi(h)
	if err != nil {
		return false
	}
	return pid == 0 || project.PIDAlive(pid)
}

func live(path string) (string, bool) {
	h, ok := holder(path)
	return h, ok && alive(h)
}

func create(path string, pid int) bool {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return false
	}
	defer f.Close()
	fmt.Fprintf(f, "%d %d000\n", pid, time.Now().Unix())
	return true
}

// claim creates path for pid, reclaiming it from a dead holder; false when a live holder (or another reclaimer) has it.
func claim(path string, pid int) bool {
	if create(path, pid) {
		return true
	}
	h, ok := holder(path)
	if !ok {
		return create(path, pid)
	}
	if alive(h) {
		return false
	}
	guard := path + ".reclaim"
	if os.Mkdir(guard, 0o755) != nil {
		fi, err := os.Stat(guard)
		if err != nil || time.Since(fi.ModTime()) <= 10*time.Second {
			return false
		}
		os.Remove(guard)
		if os.Mkdir(guard, 0o755) != nil {
			return false
		}
	}
	defer os.Remove(guard)
	if now, ok := holder(path); ok && now == h {
		os.Remove(path)
	}
	return create(path, pid)
}

// running: live worker slots plus live merge markers of every project, the legacy merge-running included.
func (s Slots) running() int {
	n := 0
	for i := 0; i < s.Max; i++ {
		if _, ok := live(filepath.Join(s.Dir, "slot-"+strconv.Itoa(i))); ok {
			n++
		}
	}
	markers, _ := filepath.Glob(filepath.Join(s.Dir, "merge-running-*"))
	for _, m := range append([]string{filepath.Join(s.Dir, "merge-running")}, markers...) {
		if !strings.HasSuffix(m, ".reclaim") {
			if _, ok := live(m); ok {
				n++
			}
		}
	}
	return n
}

// young: the marker's epoch ms field is less than BlockMax ago (missing or not a number: young).
func (s Slots) young(path string) bool {
	b, _ := os.ReadFile(path)
	line, _, _ := strings.Cut(string(b), "\n")
	_, rest, ok := strings.Cut(line, " ")
	field, _, _ := strings.Cut(rest, " ")
	ms, err := strconv.ParseInt(field, 10, 64)
	if !ok || err != nil || strings.HasPrefix(field, "+") {
		return true
	}
	return time.Now().Unix()-ms/1000 < int64(s.BlockMax/time.Second)
}

// Try is one attempt for prio (worker or merge): the claimed (or held) path, or "" and why it waits; an ErrNoDir error
// when the slot dir cannot be created.
func (s Slots) Try(prio string, pid int) (path, wait string, err error) {
	if s.Held != "" {
		if _, ok := live(s.Held); ok {
			return s.Held, "", nil
		}
	}
	if os.MkdirAll(s.Dir, 0o755) != nil {
		return "", "", fmt.Errorf("%w %s", ErrNoDir, s.Dir)
	}
	marker := filepath.Join(s.Dir, s.Marker)
	if prio == "merge" {
		if claim(marker, pid) {
			return marker, "", nil
		}
		h, ok := holder(marker)
		if !ok {
			h = "undefined"
		}
		return "", "waiting for the other merge check (pid " + h + ")", nil
	}
	if h, ok := live(marker); ok && s.young(marker) {
		return "", "waiting: a merge check is running (pid " + h + ")", nil
	}
	if s.running() < s.Max {
		for i := 0; i < s.Max; i++ {
			if p := filepath.Join(s.Dir, "slot-"+strconv.Itoa(i)); claim(p, pid) {
				return p, "", nil
			}
		}
	}
	return "", "waiting for a check slot (" + strconv.Itoa(s.running()) + " running)", nil
}

// Acquire tries until it gets a slot, a wait line on w every Report; ErrNoDir, or the context's error.
func (s Slots) Acquire(ctx context.Context, prio string, pid int, w io.Writer) (string, error) {
	poll, report := s.Poll, s.Report
	if poll <= 0 {
		poll = time.Second
	}
	if report <= 0 {
		report = 30 * time.Second
	}
	var next time.Time
	for {
		path, wait, err := s.Try(prio, pid)
		if path != "" || err != nil {
			return path, err
		}
		if now := time.Now(); !now.Before(next) {
			fmt.Fprintln(w, wait)
			next = now.Add(report)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(poll):
		}
	}
}

// Release removes path when it still holds pid.
func Release(path string, pid int) {
	if h, ok := holder(path); ok && path != "" && h == strconv.Itoa(pid) {
		os.Remove(path)
	}
}
