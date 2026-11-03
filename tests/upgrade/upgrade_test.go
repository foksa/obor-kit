package upgrade

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestUpgradeCheck is `make upgrade-check`: the previous release (OBOR_UPGRADE_PREV, else the catalog's newest version
// below VERSION; v0.23.0 or later, else refused; its binary fetched into the cache when missing) upgraded to this checkout's build (dist/obor;
// OBOR_UPGRADE_NEW another binary), with this checkout's guide. Skipped unless OBOR_UPGRADE_CHECK=1.
func TestUpgradeCheck(t *testing.T) {
	if os.Getenv("OBOR_UPGRADE_CHECK") != "1" {
		t.Skip("the release's upgrade check: make upgrade-check")
	}
	kit := kitRoot(t)
	b, err := os.ReadFile(filepath.Join(kit, "VERSION"))
	if err != nil {
		t.Fatal(err)
	}
	next := strings.TrimSpace(string(b))
	prev := os.Getenv("OBOR_UPGRADE_PREV")
	if prev == "" {
		catalog, _ := os.ReadFile(filepath.Join(kit, "bin", "checksums.txt"))
		var ok bool
		if prev, ok = previous(string(catalog), next); !ok {
			t.Fatalf("FAIL upgrade check: bin/checksums.txt has no release below %s", next)
		}
	}
	c, err := newCheck(t, kit, prev, next, true)
	if err != nil {
		t.Fatalf("FAIL upgrade check v%s → v%s: %v", prev, next, err)
	}
	c.newBin = filepath.Join(kit, "dist", "obor")
	if nb := os.Getenv("OBOR_UPGRADE_NEW"); nb != "" {
		c.newBin = nb
	}
	if _, err := os.Stat(c.newBin); err != nil {
		t.Fatalf("FAIL upgrade check v%s → v%s: no new build (%v): make build", prev, next, err)
	}
	if err := c.run(t); err != nil {
		t.Fatalf("FAIL upgrade check v%s → v%s: %v", prev, next, err)
	}
	fmt.Printf("upgrade check v%s → v%s: OK\n", prev, next)
}

func kitRoot(t testing.TB) string {
	kit, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return kit
}

// newCheck is the check of prev → next in kit: the previous binary (fetched when fetch), its tag's tree, the new
// minor's guide.
func newCheck(t testing.TB, kit, prev, next string, fetch bool) (check, error) {
	c := check{prev: prev, next: next, newKit: kit, log: t.Logf}
	if err := supported(prev); err != nil {
		return c, err
	}
	var err error
	if c.prevBin, err = release(kit, prev, fetch); err != nil {
		return c, err
	}
	if c.prevKit, err = tagTree(t, kit, prev); err != nil {
		return c, err
	}
	n, ok := parseVersion(next)
	if !ok {
		return c, fmt.Errorf("VERSION %q is not x.y.z", next)
	}
	g, err := os.ReadFile(filepath.Join(kit, "docs", fmt.Sprintf("UPGRADE-v%d.%d.md", n[0], n[1])))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return c, err
	}
	c.guide = string(g)
	return c, nil
}

// minPrev is the oldest previous release the check supports (T-778): its fixture's spec names its model (Model:,
// v0.23.0 on; before, a spec named a lane type).
const minPrev = "0.23.0"

// supported refuses a previous release the check does not support: not x.y.z, or below minPrev. Nothing is fetched.
func supported(prev string) error {
	p, ok := parseVersion(prev)
	if !ok {
		return fmt.Errorf("PREV=%s is not x.y.z", prev)
	}
	if m, _ := parseVersion(minPrev); less(p, m) {
		return fmt.Errorf("PREV=%s is not supported: the upgrade check runs from a previous release of v%s or later "+
			"(PREV=x.y.z with x.y.z >= %s; its fixture's spec names Model:)", prev, minPrev, minPrev)
	}
	return nil
}

// offline is the check of prev → next with both release binaries from the cache (no fetch: the gate has no network);
// skipped when either is not cached.
func offline(t *testing.T, prev, next string) check {
	t.Helper()
	kit := kitRoot(t)
	c, err := newCheck(t, kit, prev, next, false)
	if err == nil {
		c.newBin, err = release(kit, next, false)
	}
	if err != nil {
		t.Skipf("v%s → v%s needs both release binaries cached (make upgrade-check fetches them): %v", prev, next, err)
	}
	return c
}

// T-778: a previous release below v0.23.0 (PREV=0.22.0) is refused before anything is fetched or run, naming the
// supported range; v0.23.0 and later pass the range check.
func TestUpgradeCheckRefusesOldPrev(t *testing.T) {
	t.Parallel()
	kit := t.TempDir() // no catalog, no tags: a refusal that got past the range check would fail differently
	_, err := newCheck(t, kit, "0.22.0", "0.23.1", true)
	if err == nil {
		t.Fatal("PREV=0.22.0 accepted")
	}
	want := "PREV=0.22.0 is not supported: the upgrade check runs from a previous release of v0.23.0 or later"
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("got %q, want it to name the range (%q)", err, want)
	}
	for _, v := range []string{"0.23.0", "0.23.1", "1.0.0"} {
		if err := supported(v); err != nil {
			t.Errorf("PREV=%s: %v, want supported", v, err)
		}
	}
	if err := supported("0.9.9"); err == nil {
		t.Error("PREV=0.9.9 supported")
	}
}

// T-772: a project made by v0.23.0 survives v0.23.1 installed over it (both release binaries, the kit's guide).
func TestUpgradeV0230ToV0231(t *testing.T) {
	t.Parallel()
	c := offline(t, "0.23.0", "0.23.1")
	if err := c.run(t); err != nil {
		t.Fatal(err)
	}
}

// T-772: a new build that refuses the old config until the guide's steps ran fails the check at step 3 (its serve is
// not up, with the refusal in the output) when the guide says no steps, and passes once the guide's v0.23.1 block runs
// them.
func TestUpgradeBrokenBuild(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, guide string
		fails       bool
	}{
		{"no steps in the guide: step 3 fails", "## v0.23.1\nNothing to do.\n", true},
		{"the guide's steps: passes", "## v0.23.1\n```sh\ntouch .obor/upgraded\n```\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := offline(t, "0.23.0", "0.23.1")
			c.newBin, c.guide = refusing(t, c.newBin), tc.guide
			err := c.run(t)
			if !tc.fails {
				if err != nil {
					t.Fatalf("the guide's steps ran, want a pass: %v", err)
				}
				return
			}
			var se *stepErr
			if !errors.As(err, &se) {
				t.Fatalf("want a step's failure, got %v", err)
			}
			msg := err.Error()
			if se.step != 3 || !strings.HasPrefix(msg, "step 3: serve of alpha on the new binary is not up") ||
				!strings.Contains(msg, refusal) {
				t.Fatalf("want step 3: serve of alpha not up, with the build's refusal in the output; got:\n%s", msg)
			}
			t.Logf("the check's failure:\n%s", msg)
		})
	}
}

// refusal is what refusing's build says to a project whose upgrade steps did not run.
const refusal = "FAIL this config is older than this obor: run the upgrade steps"

// refusing is a build over bin that refuses every command but its hooks in a project without .obor/upgraded (what
// the guide's steps leave).
func refusing(t *testing.T, bin string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "obor")
	script := "#!/bin/sh\ncase $1 in hook|version) ;; *) [ -f .obor/upgraded ] || { echo '" + refusal + "' >&2; exit 1; } ;; esac\n" +
		"exec '" + bin + "' \"$@\"\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// T-772: the guide's steps of an upgrade are the sh blocks under its version headings above the previous release up to
// the new one; a new minor needs its x.y.0 heading's block.
func TestGuideSteps(t *testing.T) {
	guide := "# Upgrading\n\n## v1.2.4 (old)\n```sh\necho four\n```\n\n## v1.2.5\nText.\n```sh\necho five\n```\n" +
		"```bash\necho not a step\n```\n## v1.2.6\n```sh\necho six\n```\n"
	for _, tc := range []struct {
		name, guide, prev, next string
		want                    []string // "<version>: <text>"
		err                     string
	}{
		{"the blocks above prev up to next, sh only", guide, "1.2.4", "1.2.5", []string{"1.2.5: echo five\n"}, ""},
		{"several releases at once, in order", guide, "1.2.3", "1.2.6",
			[]string{"1.2.4: echo four\n", "1.2.5: echo five\n", "1.2.6: echo six\n"}, ""},
		{"a patch without a block: no steps", "## v1.2.5\nRe-run the installer.\n", "1.2.4", "1.2.5", nil, ""},
		{"a new minor without its block: refused", "## v1.3.0\nRe-run the installer.\n", "1.2.9", "1.3.0", nil,
			"v1.3.0 changes the minor, and the guide has no fenced sh block of its steps under its v1.3.0 heading"},
		{"a new minor with its block", "## v1.3.0 (x)\n```sh\n# nothing to do\n```\n", "1.2.9", "1.3.0",
			[]string{"1.3.0: # nothing to do\n"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			blocks, err := guideSteps(tc.guide, tc.prev, tc.next)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("want the refusal %q, got %v (blocks %v)", tc.err, err, blocks)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, b := range blocks {
				got = append(got, b.version+": "+b.text)
			}
			if strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Fatalf("steps %q, want %q", got, tc.want)
			}
		})
	}
}

// T-772: the kit's own v0.23 guide says v0.23.0's steps in its block (obor setup migrate), and v0.23.1 has none.
func TestGuideV023(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(kitRoot(t), "docs", "UPGRADE-v0.23.md"))
	if err != nil {
		t.Fatal(err)
	}
	blocks, err := guideSteps(string(b), "0.22.0", "0.23.1")
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 1 || blocks[0].version != "0.23.0" || !strings.Contains(blocks[0].text, "obor setup migrate") {
		t.Fatalf("v0.22.0 → v0.23.1 steps %+v, want v0.23.0's block with obor setup migrate", blocks)
	}
	if blocks, err := guideSteps(string(b), "0.23.0", "0.23.1"); err != nil || len(blocks) != 0 {
		t.Fatalf("v0.23.0 → v0.23.1 steps %+v (%v), want none", blocks, err)
	}
}

// T-772: the previous release is the catalog's newest x.y.z below the new version (dev builds are no release).
func TestPrevious(t *testing.T) {
	catalog := "aa  dist/obor-0.6.4-dev-darwin-arm64\nbb  dist/obor-0.23.0-darwin-arm64\ncc  dist/obor-0.22.0-linux-amd64\n" +
		"dd  dist/obor-0.23.1-darwin-arm64\n"
	for next, want := range map[string]string{"0.23.1": "0.23.0", "0.23.2": "0.23.1", "0.24.0": "0.23.1", "0.23.0": "0.22.0"} {
		if got, ok := previous(catalog, next); !ok || got != want {
			t.Errorf("previous of %s = %q (%v), want %s", next, got, ok, want)
		}
	}
	if got, ok := previous(catalog, "0.22.0"); ok {
		t.Errorf("previous of 0.22.0 = %q, want none", got)
	}
}
