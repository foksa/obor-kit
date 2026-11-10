package checks

import (
	"context"
	"fmt"
	"obor/internal/app/port"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// sx is the fixture with obor serve's Scratch on it; arm (the tests that cut git on purpose) puts a shim in front of
// git that logs each call and fails the cut-th one (exit 128, as a git I/O error). Every other test runs plain git.
type sx struct {
	*fx
	s     Scratch
	cut   string // the shim's dir: count, cut, calls
	serve string // serve's worktree
	real  string // the real git the shim execs
}

func newSx(t testing.TB, edits ...func(map[string]any)) *sx {
	t.Helper()
	return newSxEnv(t, nil, edits...)
}

// newSxEnv is newSx with env added to the project's environment (what serve's Env answers).
func newSxEnv(t testing.TB, env []string, edits ...func(map[string]any)) *sx {
	t.Helper()
	x := newFx(t, edits...)
	x.env = append(x.env, env...)
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(x.Base, "cut")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return &sx{fx: x, s: Scratch{Options: x.opts()}, cut: dir, serve: x.scratch, real: real}
}

// arm installs the shim, restarts its count and log, and fails its n-th call (0: none).
func (y *sx) arm(n int) {
	shim := y.stub("git-cut", fmt.Sprintf(`n=$(( $(cat '%[1]s/count' 2>/dev/null || echo 0) + 1 ))
echo $n > '%[1]s/count'
printf '%%s\n' "$*" | tr '\n' ' ' >> '%[1]s/calls'; echo >> '%[1]s/calls'
if [ "$n" = "$(cat '%[1]s/cut' 2>/dev/null)" ]; then echo "cut: git call $n" >&2; exit 128; fi
exec '%[2]s' "$@"`, y.cut, y.real))
	y.s.Git = shim
	y.Write(filepath.Join(y.cut, "count"), "0")
	y.Write(filepath.Join(y.cut, "cut"), strconv.Itoa(n))
	y.Write(filepath.Join(y.cut, "calls"), "")
}

// calls is the git calls since arm, one per line (a message's newlines as spaces).
func (y *sx) calls() []string { return lines(y.Read(filepath.Join(y.cut, "calls"))) }

var ctx = context.Background()

// kitDir is this checkout: the kit's githooks/ (the project's chained dir runs through them).
var kitDir = func() string { d, _ := filepath.Abs("../.."); return d }()

func (y *sx) submit() string {
	y.t.Helper()
	sha, err := y.s.Submit(ctx, "T-05", "1", "L-01", "feature")
	if err != nil {
		y.t.Fatalf("submit: %v", err)
	}
	return sha
}

func (y *sx) gate(in port.GateInput) port.GateOutcome {
	y.t.Helper()
	o, err := y.s.Gate(ctx, in)
	if err != nil {
		y.t.Fatalf("gate %+v: %v\noutput:\n%s", in, err, y.out.String())
	}
	return o
}

func (y *sx) green(in port.GateInput) port.Candidate {
	y.t.Helper()
	o := y.gate(in)
	if o.Kind != port.OutcomeGreen {
		y.t.Fatalf("gate %+v: %s %q; want green\noutput:\n%s", in, o.Kind, o.Line, y.out.String())
	}
	return o.Candidate
}

func lane(sha string) port.GateInput {
	return port.GateInput{Task: "T-05", Lane: "L-01", Kind: port.InputLane, Input: sha, Summary: "feature"}
}

// conflicted makes the lane and main both edit shared.txt, submits and gates it: the conflict open in serve's worktree.
// The submitted sha, the resolution's input.
func (y *sx) conflicted() string {
	y.t.Helper()
	y.Write(filepath.Join(y.lane, "shared.txt"), "lane\n")
	y.Write("shared.txt", "main\n")
	y.Git(y.Root, "commit", "-qam", "main edit")
	sha := y.submit()
	o := y.gate(lane(sha))
	if o.Kind != port.OutcomeConflict {
		y.t.Fatalf("precondition: conflict, got %s %q", o.Kind, o.Line)
	}
	if got := strings.Join(o.Files, " "); got != "shared.txt" {
		y.t.Fatalf("precondition: the conflicting file %q, want shared.txt", got)
	}
	return sha
}

// resolveServe resolves the open conflict in serve's worktree and commits it (not pinned).
func (y *sx) resolveServe() string {
	y.t.Helper()
	y.Write(filepath.Join(y.serve, "shared.txt"), "both\n")
	y.Git(y.serve, "add", "shared.txt")
	y.Git(y.serve, "commit", "-qm", "Merge T-05: resolved")
	return y.head(y.serve)
}

// pins is the task's pins, "submitted=<sha> candidate=… resolved=…" ("-": none); submitted is its first submit's.
func (y *sx) pins(task string) string {
	var out []string
	for _, ref := range []string{submittedRefs, candidateRefs, resolvedRefs} {
		name := ref + task
		if ref == submittedRefs {
			name += "/1"
		}
		v := y.ref(name)
		if v == "" {
			v = "-"
		}
		out = append(out, strings.TrimPrefix(strings.TrimSuffix(ref, "/"), "refs/obor/")+"="+v)
	}
	return strings.Join(out, " ")
}
