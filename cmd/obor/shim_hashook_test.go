package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"obor/internal/testkit"
)

// bin/obor's has_hook (T-279): `obor hook pre-tool-use` through the shim of a kit copy whose dist/obor is a fake, run
// without a process group of its own. A hanging `has` is bounded at 2 s with no job control: denied with exactly the
// hint, nothing else on stderr (no setpgid line), the fake killed and no watchdog sleep left behind.
func TestShimHasHook(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, has string // has: the fake's `has hook` body (the fast ones wait for the watchdog's sleep: see started)
		code      int
		hint      bool // stderr is exactly the hint; else empty
		ran       bool // the fake's hook ran (stdout "go ran")
	}{
		{"hanging binary: hint after 2 s, killed, no stray sleep", `echo $$ > "$dir/has.pid"; exec /bin/sleep 30`, 2, true, false},
		{"stale binary: the hint only", started + `; exit 1`, 2, true, false},
		{"good binary: runs Go", started + `; exit 0`, 0, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := testkit.New(t)
			p.Write(".obor/config.json", "{}\n")
			kit := filepath.Join(p.Base, "kit")
			for _, f := range []string{"bin/obor", "bin/go-commands", "VERSION"} {
				b, err := os.ReadFile(filepath.Join("..", "..", f))
				if err != nil {
					t.Fatal(err)
				}
				writeExec(t, filepath.Join(kit, f), string(b))
			}
			writeExec(t, filepath.Join(kit, "dist", "obor"), "#!/bin/bash\ndir=$(dirname \"$0\")\n"+
				"if [ \"$1\" = has ]; then "+tc.has+"; fi\n"+
				"[ \"$1 $2\" = 'hook pre-tool-use' ] && echo 'go ran'\n")
			// sleep on PATH records each pid, so a watchdog sleep outliving the shim is seen.
			fakes := filepath.Join(p.Base, "fakes")
			sleeps := filepath.Join(p.Base, "sleep.pids")
			writeExec(t, filepath.Join(fakes, "sleep"), "#!/bin/bash\necho $$ >> "+sleeps+"\nexec /bin/sleep \"$@\"\n")

			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "/bin/bash", filepath.Join(kit, "bin", "obor"), "hook", "pre-tool-use")
			cmd.Dir = p.Base
			cmd.Env = []string{"HOME=" + filepath.Join(p.Base, "home"), "PATH=" + fakes + ":/usr/bin:/bin:/usr/sbin:/sbin"}
			cmd.Stdin = strings.NewReader(`{"tool_name":"Edit","cwd":"` + p.Root + `","tool_input":{"file_path":"x"}}`)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			start := time.Now()
			err := cmd.Run()
			elapsed := time.Since(start)
			code := 0
			if ee := (*exec.ExitError)(nil); errors.As(err, &ee) {
				code = ee.ExitCode()
			} else if err != nil {
				t.Fatal(err)
			}

			if code != tc.code {
				t.Errorf("exit %d, want %d (stderr %q)", code, tc.code, stderr.String())
			}
			want := ""
			if tc.hint {
				want = "obor: no usable Go binary in " + kit + " — run: make -C " + kit + " build\n"
			}
			if stderr.String() != want {
				t.Errorf("stderr %q, want %q", stderr.String(), want)
			}
			if got := strings.TrimSpace(stdout.String()) == "go ran"; got != tc.ran {
				t.Errorf("hook ran %v, want %v (stdout %q)", got, tc.ran, stdout.String())
			}
			// The watchdog started its sleep in every case; none outlives the shim.
			pids := readPids(t, sleeps)
			if len(pids) == 0 {
				t.Error("no watchdog sleep recorded (precondition)")
			}
			for _, pid := range pids {
				if !gone(pid) {
					t.Errorf("watchdog sleep %d still running after the shim", pid)
				}
			}
			if strings.HasPrefix(tc.name, "hanging") {
				// The 2 s bound plus the shim's own work (git, jq: over 1 s on a loaded machine); the fake sleeps 30 s.
				if elapsed < 2*time.Second || elapsed > hangBound {
					t.Errorf("took %v, want the 2 s bound", elapsed)
				}
				pids := readPids(t, filepath.Join(kit, "dist", "has.pid"))
				if len(pids) != 1 {
					t.Fatalf("fake has pids %v, want one (precondition)", pids)
				}
				if !gone(pids[0]) {
					t.Errorf("hanging fake %d still running", pids[0])
				}
			}
		})
	}
}

// started waits (at most 10 s) until the watchdog's sleep has recorded its pid in sleep.pids (beside the kit), so the
// fake returns while that sleep runs, however slow the machine.
const started = `n=0; while [ ! -s "$dir/../../sleep.pids" ] && [ $n -lt 1000 ]; do /bin/sleep 0.01; n=$((n+1)); done`

func readPids(t *testing.T, path string) []int {
	t.Helper()
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		t.Fatal(err)
	}
	var pids []int
	for _, f := range strings.Fields(string(b)) {
		pid, err := strconv.Atoi(f)
		if err != nil {
			t.Fatal(err)
		}
		pids = append(pids, pid)
	}
	return pids
}

// gone: pid has exited within 500 ms (re-parented zombies are reaped by init shortly after), well under the 2 s a
// leftover watchdog sleep would live.
func gone(pid int) bool {
	for deadline := time.Now().Add(500 * time.Millisecond); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
			return true
		}
	}
	return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
}
