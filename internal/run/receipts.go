package run

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"obor/internal/config"
	"obor/internal/project"
)

// Test receipts (T-349): a heavy check that `obor run` saw end green in a lane leaves
// .obor/state/receipts/<tree>-<check> = "<tree> <check> <sha256 of the command text> <epoch> <lane> <secs>s". The
// tree is the lane's content tree as serve's gate would commit it; the gate reuses the run when its merge tree, check
// and command are the same and the receipt is young enough. Only obor run writes one (obor's measurement, not the
// worker's claim); it is keyed by content, so it cannot be stale.

// ReceiptDir is where the receipts of the project whose state dir is state live.
func ReceiptDir(state string) string { return filepath.Join(state, "receipts") }

// receiptName is the check names a receipt file can carry (a command name with a path separator or a space is no
// receipt).
var receiptName = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// CmdHash is the sha256 of a command's text: a changed config line is no receipt.
func CmdHash(c config.Command) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(c.Cmd))) }

// WriteReceipt records a green run of c on tree by lane. Receipts in the dir older than maxAge are removed (FindReceipt
// ignores them anyway). A name that cannot be a file name writes nothing.
func WriteReceipt(state, tree string, c config.Command, lane string, took time.Duration, now time.Time, maxAge time.Duration) error {
	if tree == "" || !receiptName.MatchString(c.Name) {
		return nil
	}
	dir := ReceiptDir(state)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	line := fmt.Sprintf("%s %s %s %d %s %ds\n", tree, c.Name, CmdHash(c), now.Unix(), lane, int(took.Seconds()))
	tmp, err := os.CreateTemp(dir, ".tmp-")
	if err != nil {
		return err
	}
	_, err = tmp.WriteString(line)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), filepath.Join(dir, tree+"-"+c.Name))
	}
	if err != nil {
		os.Remove(tmp.Name())
		return err
	}
	PruneOlder(dir, now.Add(-maxAge))
	return nil
}

// PruneOlder removes the files in dir last modified before cutoff (a dot file is a write in flight).
func PruneOlder(dir string, cutoff time.Time) {
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if info, err := e.Info(); err == nil && !strings.HasPrefix(e.Name(), ".") && info.Mode().IsRegular() && info.ModTime().Before(cutoff) {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// FindReceipt is the lane that proved c green on tree, when a receipt for exactly (tree, c.Name, c's text) is younger
// than maxAge (a receipt from the future is not trusted).
func FindReceipt(state, tree string, c config.Command, maxAge time.Duration, now time.Time) (lane string, ok bool) {
	if tree == "" || !receiptName.MatchString(c.Name) {
		return "", false
	}
	b, err := os.ReadFile(filepath.Join(ReceiptDir(state), tree+"-"+c.Name))
	if err != nil {
		return "", false
	}
	f := strings.Fields(string(b))
	if len(f) < 5 || f[0] != tree || f[1] != c.Name || f[2] != CmdHash(c) {
		return "", false
	}
	epoch, err := strconv.ParseInt(f[3], 10, 64)
	if err != nil {
		return "", false
	}
	age := now.Sub(time.Unix(epoch, 0))
	if age < 0 || age >= maxAge {
		return "", false
	}
	return f[4], true
}

// Tree is the content tree of the work in dir (a worktree): HEAD plus every tracked and untracked change, as serve's
// submit commits a lane (add -A), the run marker excluded. It is computed with a temporary index, so dir's index and
// history are not touched.
func Tree(dir string, environ []string) (string, error) {
	tmp, err := os.MkdirTemp("", "obor-tree-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	if environ == nil {
		environ = os.Environ()
	}
	env := append(append([]string(nil), environ...), "GIT_INDEX_FILE="+filepath.Join(tmp, "index"))
	git := func(args ...string) (string, error) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = env
		out, err := cmd.Output()
		return strings.TrimSpace(string(out)), err
	}
	for _, args := range [][]string{{"read-tree", "HEAD"}, {"add", "-A"},
		{"reset", "-q", "--", MarkerName}} {
		if _, err := git(args...); err != nil {
			return "", fmt.Errorf("git %s in %s: %w", args[0], dir, err)
		}
	}
	return git("write-tree")
}

// ChangedFiles is the files a tree changes against base (names only, a rename as a deletion and an addition: the
// merge's own listing, RV-03 #2).
func ChangedFiles(dir, base, tree string) ([]string, error) {
	out, err := project.Git(dir, "diff", "--no-renames", "--name-only", base, tree)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, l := range strings.Split(out, "\n") {
		if l != "" {
			files = append(files, l)
		}
	}
	return files, nil
}

// CPUs is CHECK_CPUS: slots.cpus when set, else cores / slots.max (CHECK_SLOTS), at least 1. Two checks at Go's default
// parallelism put twice the cores' jobs on the machine; the project maps it (go test -p).
func CPUs(cfg *config.Config, env project.Env, cores int) int {
	if s := cfg.Data.Slots; s != nil && s.CPUs != nil {
		return *s.CPUs
	}
	return max(1, cores/max(1, SlotsFor(cfg, env).Max))
}

// CPUsEnv is CPUs for this machine as an environment entry.
func CPUsEnv(cfg *config.Config, env project.Env) string {
	return "CHECK_CPUS=" + strconv.Itoa(CPUs(cfg, env, runtime.NumCPU()))
}
