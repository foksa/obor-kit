package upgrade

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestUpgradeCheck is `make upgrade-check`: the previous release (OBOR_UPGRADE_PREV, else the shipped catalog's newest
// version below VERSION; its binary fetched into the cache when missing) upgraded to this checkout's build (dist/obor;
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
		var ok bool
		if prev, ok = previous(shippedCatalog(t, kit), next); !ok {
			t.Fatalf("FAIL upgrade check: the shipped catalog has no release below %s", next)
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

// the guide's steps of an upgrade are the sh blocks under its version headings above the previous release up to
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

// the previous release is the catalog's newest x.y.z below the new version (dev builds are no release).
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

// A version tagged locally and never shipped is no release: the shipped catalog stops at 0.40.1, while HEAD's catalog
// has the abandoned 0.40.2's lines too. The check's previous release of 0.40.3 is 0.40.1.
func TestPreviousSkipsAnUnshippedVersion(t *testing.T) {
	shipped := "aa  dist/obor-0.40.0-linux-amd64\nbb  dist/obor-0.40.1-darwin-arm64\n"
	head := "cc  dist/obor-0.40.2-darwin-arm64\n" + shipped
	// Precondition: HEAD's catalog does name the abandoned tag, else a check reading it would not pick 0.40.2.
	if got, ok := previous(head, "0.40.3"); !ok || got != "0.40.2" {
		t.Fatalf("precondition: previous in HEAD's catalog = %q (%v), want the abandoned 0.40.2", got, ok)
	}
	if got, ok := previous(shipped, "0.40.3"); !ok || got != "0.40.1" {
		t.Errorf("previous in the shipped catalog = %q (%v), want 0.40.1", got, ok)
	}
}

// shippedCatalog reads the users' catalog from origin/main, which `make ship` pushes; when git has no origin/main (a
// fork, a fixture repo without the remote) it is HEAD's, this checkout's bin/checksums.txt.
func TestShippedCatalog(t *testing.T) {
	kit := t.TempDir()
	head := "hh  dist/obor-0.40.1-darwin-arm64\n"
	if err := os.MkdirAll(filepath.Join(kit, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(kit, "bin", "checksums.txt"), []byte(head), 0o644); err != nil {
		t.Fatal(err)
	}
	// A stub git, first on PATH: it logs the question and answers with origin/main's catalog, or exits 1 as `git show`
	// does without the ref. The budget run counts real git processes only, and this needs no repository.
	stub, calls := filepath.Join(kit, "stub", "git"), filepath.Join(kit, "stub", "calls")
	t.Setenv("PATH", filepath.Join(kit, "stub")+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, tc := range []struct {
		name, shipped, want string
	}{
		{"origin/main's catalog", "ss  dist/obor-0.40.0-darwin-arm64\n", "ss  dist/obor-0.40.0-darwin-arm64\n"},
		{"no origin/main: HEAD's catalog", "", head},
	} {
		t.Run(tc.name, func(t *testing.T) {
			script := "#!/bin/sh\necho \"$*\" > " + calls + "\n"
			if tc.shipped == "" {
				script += "exit 1\n"
			} else {
				script += "printf '%s' '" + tc.shipped + "'\n"
			}
			if err := os.MkdirAll(filepath.Dir(stub), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			if got := shippedCatalog(t, kit); got != tc.want {
				t.Errorf("shippedCatalog = %q, want %q", got, tc.want)
			}
			asked, err := os.ReadFile(calls)
			if err != nil {
				t.Fatal(err)
			}
			if got, want := string(asked), "-C "+kit+" show origin/main:bin/checksums.txt\n"; got != want {
				t.Errorf("git was asked %q, want %q", got, want)
			}
		})
	}
}
