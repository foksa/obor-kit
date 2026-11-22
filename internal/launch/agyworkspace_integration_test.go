//go:build integration

package launch_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"obor/internal/launch"
	"obor/internal/project"
	agykind "obor/kinds/agy/kind"
)

// agyRoles are the role agents whose cwd is their role worktree, by role.
var agyRoles = []struct{ role, agent, pane string }{
	{"integrator", "pj-int", "w1:p5"},
	{"reviewer", "pj-rev", "w1:p7"},
	{"deep_dive", "pj-deep-dive", "w1:p9"},
}

// agyGit is agyOn on a real git project whose main branch commits the main checkout's current agy hooks file.
func agyGit(t *testing.T) *fx {
	t.Helper()
	x := agyOn(t, setupGit, currentHooks())
	x.p.Git(x.p.Root, "add", "-f", agykind.HooksFile)
	x.p.Git(x.p.Root, "commit", "-q", "-m", "agy's hooks")
	return x
}

// A role agent on agy whose role worktree is absent: launch adds it at the head it uses (merge.target_branch's), the
// kind's hooks check reads the added worktree's file (main's, current), and the agent starts there.
func TestAgyRoleAgentMissingWorktreeIsCreatedCheckedStarts(t *testing.T) {
	t.Parallel()
	for _, r := range agyRoles {
		t.Run(r.role, func(t *testing.T) {
			t.Parallel()
			x := agyGit(t)
			wt := x.p.Cfg().RoleWorktree(r.role)
			if _, err := os.Stat(wt); !os.IsNotExist(err) {
				t.Fatalf("precondition: no role worktree %s: %v", wt, err)
			}
			x.shell(r.pane, r.agent, "")
			out, ok := x.restart(launch.Request{Agent: r.agent, Fresh: true})
			if !ok {
				t.Fatalf("restart failed:\n%s", out)
			}
			contains(t, x.startCall(), "kind=agy", "the start")
			if top := strings.TrimSpace(x.p.Git(wt, "rev-parse", "--show-toplevel")); project.Physical(top) != project.Physical(wt) {
				t.Fatalf("the role worktree %s was not added (toplevel %q)", wt, top)
			}
			eq(t, strings.TrimSpace(x.p.Git(wt, "rev-parse", "HEAD")), strings.TrimSpace(x.p.Git(x.p.Root, "rev-parse", "main")),
				"the added worktree's head (main's)")
			eq(t, x.p.Read(filepath.Join(wt, agykind.HooksFile)), currentHooks(), "the hooks file the check read")
		})
	}
}

// A role agent's worktree there at a commit whose hooks file is stale: its start is refused; once the worktree is moved
// to the integration branch's head (as serve's Detach moves it: launch moves none), the check reads the moved tree and
// the agent starts.
func TestAgyRoleAgentWorktreeMovedToHeadIsCheckedAfterTheMove(t *testing.T) {
	t.Parallel()
	for _, r := range agyRoles {
		t.Run(r.role, func(t *testing.T) {
			t.Parallel()
			x := agyOn(t, setupGit, staleHooks)
			root, wt := x.p.Root, x.p.Cfg().RoleWorktree(r.role)
			x.p.Git(root, "add", "-f", agykind.HooksFile)
			x.p.Git(root, "commit", "-q", "-m", "old hooks")
			x.p.Git(root, "worktree", "add", "-q", "--detach", wt, "main")
			x.p.Write(agykind.HooksFile, currentHooks())
			x.p.Git(root, "commit", "-q", "-am", "current hooks")
			path := filepath.Join(wt, agykind.HooksFile)
			if x.p.Read(path) != staleHooks {
				t.Fatalf("precondition: the role worktree's hooks file is stale: %s", x.p.Read(path))
			}
			x.shell(r.pane, r.agent, "")
			out, ok := x.restart(launch.Request{Agent: r.agent, Fresh: true})
			refusedFor(t, x, out, ok, "agy's hooks: "+path+"'s obor entry is not this kit's")
			x.p.Git(wt, "checkout", "-q", "--detach", "main")
			out, ok = x.restart(launch.Request{Agent: r.agent, Fresh: true})
			if !ok {
				t.Fatalf("restart after the move failed:\n%s", out)
			}
			contains(t, x.startCall(), "kind=agy", "the start")
		})
	}
}
