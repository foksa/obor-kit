//go:build release

package release

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The release's marketplace flip renames only the marketplace (top-level name obor-kit-dev →
// obor-kit), never the owner or the plugin entry; release and publish refuse a tag whose marketplace differs from main's
// in anything else. The good tag's shape is asserted in TestMakeReleaseFullRun; publish's refusals in TestMakePublish.

// main is the dev checkout, so the kit's marketplace
// is obor-kit-dev (a release tag renames it to obor-kit, which the flip above does); it lists the plugin obor at ./.
// cmd/obor keeps the manifest invariants it can check without the release's name.
func TestTreeMarketplaceIsDev(t *testing.T) {
	t.Parallel()
	var market struct {
		Name    string
		Plugins []struct{ Name, Source string }
	}
	if err := json.Unmarshal(treeFile(t, ".claude-plugin/marketplace.json"), &market); err != nil {
		t.Fatalf("marketplace.json does not parse: %v", err)
	}
	if market.Name != "obor-kit-dev" {
		t.Errorf("main's marketplace name is %q, want obor-kit-dev (a release tag renames it to obor-kit)", market.Name)
	}
	if len(market.Plugins) != 1 || market.Plugins[0].Name != "obor" || market.Plugins[0].Source != "./" {
		t.Errorf("marketplace.json lists %+v, want the one plugin obor at ./", market.Plugins)
	}
}

// publishCheck runs `sh scripts/publish <v> --check` in the fixture (the tag checks ship runs before it pushes).
func (f *releaseFixture) publishCheck(t *testing.T, v string) (string, bool) {
	t.Helper()
	cmd := exec.Command("sh", "scripts/publish", v, "--check")
	cmd.Dir = f.Root
	cmd.Env = f.env()
	out, err := cmd.CombinedOutput()
	return string(out), err != nil
}

// A tag made the v0.17.2 way (every "name" set to obor-kit, the plugin entry's too) is refused by release and by
// publish, and neither moves the tag or commits.
func TestReleaseRefusesTagWithRenamedPlugin(t *testing.T) {
	t.Parallel()
	f := newReleaseFixture(t)
	if out, failed := f.release(t, "V=1.2.4"); failed {
		t.Fatalf("make release V=1.2.4 failed:\n%s", out)
	}
	// Control: the good tag passes publish's checks, so the refusal below is the marketplace's.
	if out, failed := f.publishCheck(t, "1.2.4"); failed {
		t.Fatalf("precondition: publish --check refuses the good tag:\n%s", out)
	}
	// Rebuild the tag's commit on the same source parent with v0.17.2's marketplace.
	wt := filepath.Join(f.Base, "old-flip")
	f.Git(f.Root, "worktree", "add", "-q", "--detach", wt, "v1.2.4")
	writeFile(t, filepath.Join(wt, ".claude-plugin", "marketplace.json"), v0172Marketplace+"\n")
	f.Git(wt, "commit", "-q", "-a", "--amend", "-m", "obor v1.2.4: marketplace name, checksums")
	f.Git(f.Root, "tag", "-f", "v1.2.4", f.Git(wt, "rev-parse", "HEAD"))
	f.Git(f.Root, "worktree", "remove", "--force", wt)
	if got := f.Git(f.Root, "rev-parse", "v1.2.4^1"); got != f.Git(f.Root, "rev-parse", "main^1") {
		t.Fatalf("precondition: the rebuilt tag's parent %s is not the version commit", got)
	}
	head := f.Git(f.Root, "rev-parse", "HEAD")
	tagBefore := f.Git(f.Root, "rev-parse", "v1.2.4")

	want := "plugin name at v1.2.4 is 'obor-kit', want 'obor'"
	out, failed := f.release(t, "V=1.2.4")
	if !failed || !strings.Contains(out, want) {
		t.Errorf("release must refuse the tag naming %q (failed %v):\n%s", want, failed, out)
	}
	if got := f.Git(f.Root, "rev-parse", "HEAD"); got != head {
		t.Errorf("release committed: HEAD %s", got)
	}
	if got := f.Git(f.Root, "rev-parse", "v1.2.4"); got != tagBefore {
		t.Errorf("the tag moved from %s to %s", tagBefore, got)
	}
	out, failed = f.publishCheck(t, "1.2.4")
	if !failed || !strings.Contains(out, want) {
		t.Errorf("publish must refuse the tag naming %q (failed %v):\n%s", want, failed, out)
	}
}

// A tag whose plugin entry is right but whose owner was renamed differs from main's beyond the top-level name: refused
// by release (also as a tag commit that changes more than its catalog and marketplace name).
func TestReleaseRefusesTagWithRenamedOwner(t *testing.T) {
	t.Parallel()
	f := newReleaseFixture(t)
	if out, failed := f.release(t, "V=1.2.4"); failed {
		t.Fatalf("make release V=1.2.4 failed:\n%s", out)
	}
	wt := filepath.Join(f.Base, "owner-flip")
	f.Git(f.Root, "worktree", "add", "-q", "--detach", wt, "v1.2.4")
	writeFile(t, filepath.Join(wt, ".claude-plugin", "marketplace.json"),
		`{"name": "obor-kit","owner":{"name": "obor-kit"},"plugins":[{"name":"obor","source":"./"}]}`+"\n")
	f.Git(wt, "commit", "-q", "-a", "--amend", "-m", "obor v1.2.4: marketplace name, checksums")
	f.Git(f.Root, "tag", "-f", "v1.2.4", f.Git(wt, "rev-parse", "HEAD"))
	f.Git(f.Root, "worktree", "remove", "--force", wt)

	out, failed := f.release(t, "V=1.2.4")
	if !failed || !strings.Contains(out, "differs from its source's in more than the top-level name") {
		t.Errorf("release must refuse the renamed owner (failed %v):\n%s", failed, out)
	}
}

// A main whose flip would touch the plugin name too (the plugin entry also named obor-kit-dev: the string is there
// twice) is refused before anything is tagged, and the scratch worktree is removed.
func TestReleaseRefusesFlipTouchingPlugin(t *testing.T) {
	t.Parallel()
	f := newReleaseFixture(t)
	writeFile(t, filepath.Join(f.Root, ".claude-plugin", "marketplace.json"),
		`{"name":"obor-kit-dev","owner":{"name":"x"},"plugins":[{"name":"obor-kit-dev","source":"./"}]}`+"\n")
	f.Git(f.Root, "add", "-A")
	f.Git(f.Root, "commit", "-q", "-m", "a plugin entry named like the dev marketplace")

	out, failed := f.release(t, "V=1.2.4")
	if !failed || !strings.Contains(out, `no single "name": "obor-kit-dev"`) || !strings.Contains(out, "nothing is tagged") {
		t.Errorf("release must refuse the ambiguous flip (failed %v):\n%s", failed, out)
	}
	if got := f.Git(f.Root, "log", "--format=%s", "-1"); got != "obor v1.2.4: VERSION, plugin version" {
		t.Errorf("precondition: the release did not reach the tag step (last commit %q)", got)
	}
	if f.hasTag("v1.2.4") {
		t.Error("the ambiguous flip was tagged")
	}
	if f.Exists(f.scratch("1.2.4")) {
		t.Error("the scratch worktree is still there")
	}
}
