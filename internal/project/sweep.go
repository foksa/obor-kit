package project

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"obor/internal/config"
	"obor/internal/roles"
)

// The sweep's retentions: the newest daily backups, and how long drafts, stop-failure entries and a leftover
// backup temp file stay.
const (
	backupKeep      = 7
	draftKeep       = 14 * 24 * time.Hour
	stopFailureKeep = 14 * 24 * time.Hour
	backupTempKeep  = 24 * time.Hour
)

// staleLogKeep is how long a state file with no writer but a person's reader stays unwritten before it goes (
// launch.log, restart-<agent>.log: their failure output is worth a fortnight).
const staleLogKeep = 14 * 24 * time.Hour

// FlakeKeep is how far back obor flakes reads flakes.jsonl: the sweep drops an older entry. The checks
// package cannot be imported here, so its reader (flakeWindow) takes this window from here.
const FlakeKeep = 7 * 24 * time.Hour

// retired is the state dir's entries no current writer has, by name: a task that deletes a writer adds its file here
// (AGENTS.md), and the sweep removes each.
var retired = []string{"herdr-instance"}

// Sweep removes what the project at cfg.Root no longer needs, right after obor nightly's backup: daily backups past
// the newest backupKeep dates (either format: gzip'd days count together with the older uncompressed ones;
// every other entry of state/backup/ but a temp file younger than a day), files keyed by a lane or agent the config no
// longer names (run-changed-<lane>, lane-changed-files-<lane>, lanes/<lane>.plugin, lanes/<lane>.setup, inbox/<slot>,
// restart-<agent>.log, restart-<lane|agent>.lock), the retired state entries (retired), drafts not modified for
// draftKeep, launch.log and restart-<agent>.log unwritten for staleLogKeep, stop-failure entries older than
// stopFailureKeep and flakes.jsonl's entries older than flakeKeep. A task's step logs are serve's to remove at its
// end: the sweep leaves them, a task's or an unknown one's.
// backedUp says this nightly's backup succeeded with a nonempty reply: only then is the backup step run (a
// failed or empty backup must not remove the days an upgraded project still has). Every other step runs regardless. It
// never touches obor.db*, serve.*, serve.log, nightly's and a release's logs (their writers end them),
// kinds/, opencode.json, last-full-check or background.jsonl. Every removal is best-effort: a failure is one line
// naming its path and the sweep goes on (a removal it misses is only disk).
func Sweep(cfg *config.Config, now time.Time, backedUp bool) []string {
	if cfg == nil {
		return []string{"sweep: no config"}
	}
	state := StateDir(cfg.Root)
	steps := []struct {
		what  string
		paths func() ([]string, error)
	}{
		{"backup", func() ([]string, error) {
			if !backedUp {
				return nil, nil
			}
			return sweepBackups(filepath.Join(state, "backup"), now)
		}},
		{"lanes and agents", func() ([]string, error) { return sweepNames(cfg, state) }},
		{"retired", func() ([]string, error) {
			paths := make([]string, 0, len(retired))
			for _, name := range retired {
				paths = append(paths, filepath.Join(state, name))
			}
			return paths, nil
		}},
		{"drafts", func() ([]string, error) { return sweepDrafts(filepath.Join(cfg.Root, ".obor", "drafts"), now) }},
		{"logs", func() ([]string, error) { return sweepLogs(state, now) }},
	}
	var out []string
	for _, step := range steps {
		paths, err := step.paths()
		if err != nil {
			out = append(out, fmt.Sprintf("sweep %s: %v", step.what, err))
		}
		for _, path := range paths {
			if err := os.RemoveAll(path); err != nil {
				out = append(out, fmt.Sprintf("sweep %s: %v", step.what, err))
			}
		}
	}
	// The stop-failure log's file stays; its entries keep their own 14-day retention. flakes.jsonl's file stays
	// too: the entries obor flakes cannot read (7 days) go.
	if err := pruneStopFailure(filepath.Join(state, StopFailureLog), now); err != nil {
		out = append(out, fmt.Sprintf("sweep stop-failure log: %v", err))
	}
	if err := pruneFlakes(filepath.Join(state, "flakes.jsonl"), now); err != nil {
		out = append(out, fmt.Sprintf("sweep flakes: %v", err))
	}
	return out
}

// sweepBackups is state/backup's entries to remove: the daily backups outside the newest backupKeep dates (a date
// keeps every format it has), and every other entry (herd-*.db, an unknown leftover) but a temp file younger
// than a day.
func sweepBackups(dir string, now time.Time) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var dates []string
	for _, e := range entries {
		if date, ok := dailyBackup(e.Name()); ok && !slices.Contains(dates, date) {
			dates = append(dates, date)
		}
	}
	sort.Strings(dates) // by date: the name's only variable part
	keep := map[string]bool{}
	for i := len(dates) - 1; i >= 0 && len(keep) < backupKeep; i-- {
		keep[dates[i]] = true
	}
	var out []string
	for _, e := range entries {
		if date, ok := dailyBackup(e.Name()); ok && keep[date] {
			continue
		}
		if isBackupTemp(e.Name()) {
			if fi, err := e.Info(); err == nil && now.Sub(fi.ModTime()) < backupTempKeep {
				continue // a backup being written now
			}
		}
		out = append(out, filepath.Join(dir, e.Name()))
	}
	return out, nil
}

// dailyBackup: name is a daily backup, obor-<YYYY-MM-DD>.db.gz, or uncompressed, obor-<YYYY-MM-DD>.db
// and its date.
func dailyBackup(name string) (string, bool) {
	date, ok := strings.CutPrefix(name, "obor-")
	if !ok {
		return "", false
	}
	date, ok = strings.CutSuffix(date, ".db.gz")
	if !ok {
		date, ok = strings.CutSuffix(date, ".db")
		if !ok {
			return "", false
		}
	}
	if _, err := time.Parse("2006-01-02", date); err != nil {
		return "", false
	}
	return date, true
}

// isBackupTemp: name is a backup's temporary file (BackupDaily's .tmp and .raw).
func isBackupTemp(name string) bool {
	return strings.HasSuffix(name, ".tmp") || strings.HasSuffix(name, ".raw")
}

// sweepNames is the state dir's per-lane and per-agent files whose lane, agent or slot the config does not name. A
// name it cannot parse stays.
func sweepNames(cfg *config.Config, state string) ([]string, error) {
	lanes, agents, slots, err := liveNames(cfg)
	if err != nil {
		return nil, err
	}
	var out []string
	entries, err := os.ReadDir(state)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		switch {
		case strings.HasPrefix(name, "run-changed-"):
			if lane := strings.TrimPrefix(name, "run-changed-"); lane != "" && !lanes[lane] {
				out = append(out, filepath.Join(state, name))
			}
		case strings.HasPrefix(name, "lane-changed-files-"):
			if lane := strings.TrimPrefix(name, "lane-changed-files-"); lane != "" && !lanes[lane] {
				out = append(out, filepath.Join(state, name))
			}
		case strings.HasPrefix(name, "restart-") && strings.HasSuffix(name, ".log"):
			if agent := strings.TrimSuffix(strings.TrimPrefix(name, "restart-"), ".log"); agent != "" && !agents[agent] {
				out = append(out, filepath.Join(state, name))
			}
		case strings.HasPrefix(name, "restart-") && strings.HasSuffix(name, ".lock"):
			if who := strings.TrimSuffix(strings.TrimPrefix(name, "restart-"), ".lock"); who != "" && !lanes[who] && !agents[who] {
				out = append(out, filepath.Join(state, name))
			}
		}
	}
	paths, err := sweepDir(filepath.Join(state, "lanes"), func(name string) bool {
		for _, ext := range []string{".plugin", ".setup"} {
			if stem, ok := strings.CutSuffix(name, ext); ok {
				return stem != "" && !lanes[stem]
			}
		}
		return false // an extension no writer of a per-lane file uses: leave it
	})
	if err != nil {
		return out, err
	}
	out = append(out, paths...)
	paths, err = sweepDir(filepath.Join(state, "inbox"), func(name string) bool {
		if strings.HasPrefix(name, ".") {
			return false // WriteInbox's temp file
		}
		return name != "" && !slots[name]
	})
	if err != nil {
		return out, err
	}
	return append(out, paths...), nil
}

// liveNames is the lanes, agent names and inbox slots cfg yields now. A name it cannot derive is an error (a config
// without a prefix would otherwise make every per-agent file look gone).
func liveNames(cfg *config.Config) (lanes, agents, slots map[string]bool, err error) {
	lanes, agents, slots = map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, l := range cfg.Lanes() {
		lanes[l.Name] = true
		for _, role := range []string{"worker", roles.Driver} {
			agent := cfg.Self(role, l.Name)
			if agent == "" {
				return nil, nil, nil, fmt.Errorf("lane %s has no %s agent name (the config prefix is empty)", l.Name, role)
			}
			agents[agent] = true
			if slot := InboxSlot(cfg, Role{Role: role, Lane: l.Name}); slot != "" {
				slots[strings.ReplaceAll(slot, "/", ".")] = true
			}
		}
	}
	for _, rc := range cfg.RoleAgents() {
		if roles.LaneBound(rc.Role.Name) {
			continue
		}
		if rc.Agent == "" {
			return nil, nil, nil, fmt.Errorf("role %s has no agent name (the config prefix is empty)", rc.Role.Name)
		}
		agents[rc.Agent] = true
		if slot := InboxSlot(cfg, Role{Role: rc.Role.Name}); slot != "" {
			slots[slot] = true
		}
	}
	return lanes, agents, slots, nil
}

// sweepDir is dir's files gone says yes of; a directory it does not read is the error.
func sweepDir(dir string, gone func(name string) bool) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && gone(e.Name()) {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	return out, nil
}

// sweepLogs is the state's own logs nothing needs any more: launch.log and restart-<agent>.log unwritten for
// staleLogKeep. A task's step logs are serve's to remove at its end (mtask.ended), never the sweep's: it cannot tell a
// landed task still running its after-merge steps from a closed one. A pass's own log stays: nightly's next pass removes
// it, and the release script its own.
func sweepLogs(state string, now time.Time) ([]string, error) {
	var out []string
	cutoff := now.Add(-staleLogKeep)
	entries, err := os.ReadDir(state)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	for _, e := range entries { // the state's own logs: the names are fixed, so this loop is the age rule alone
		name := e.Name()
		if e.IsDir() || name != "launch.log" && !(strings.HasPrefix(name, "restart-") && strings.HasSuffix(name, ".log")) {
			continue
		}
		if fi, err := e.Info(); err == nil && fi.ModTime().Before(cutoff) {
			out = append(out, filepath.Join(state, name))
		}
	}
	return out, nil
}

// pruneFlakes drops flakes.jsonl's entries older than flakeKeep; the file stays, and an entry that does not parse
// stays (a crash left half of one, and its readers skip it).
func pruneFlakes(path string, now time.Time) error {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	cutoff := now.Add(-FlakeKeep).Unix()
	var kept bytes.Buffer
	dropped := false
	for _, line := range bytes.Split(b, []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var entry struct {
			Time int64 `json:"time"`
		}
		if json.Unmarshal(line, &entry) == nil && entry.Time < cutoff {
			dropped = true
			continue
		}
		kept.Write(line)
		kept.WriteByte('\n')
	}
	if !dropped {
		return nil
	}
	return os.WriteFile(path, kept.Bytes(), 0o644)
}

// sweepDrafts is .obor/drafts' files not modified for draftKeep.
func sweepDrafts(dir string, now time.Time) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	cutoff := now.Add(-draftKeep)
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if fi, err := e.Info(); err == nil && fi.ModTime().Before(cutoff) {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	return out, nil
}

// pruneStopFailure drops the stop-failure log's entries older than stopFailureKeep (its own 14-day
// retention, moved into the sweep); the file stays.
func pruneStopFailure(path string, now time.Time) error {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	cutoff := now.Add(-stopFailureKeep)
	var kept bytes.Buffer
	for _, line := range bytes.Split(b, []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var entry struct {
			At string `json:"at"`
		}
		if json.Unmarshal(line, &entry) == nil {
			if at, err := time.Parse(time.RFC3339, entry.At); err == nil && at.Before(cutoff) {
				continue
			}
		}
		kept.Write(line)
		kept.WriteByte('\n')
	}
	return os.WriteFile(path, kept.Bytes(), 0o600)
}
