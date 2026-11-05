package guard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"obor/internal/config"
	"obor/internal/project"
	"obor/internal/store"
	"obor/internal/testkit"
)

// fx is the guard's fixture project: proj (main) with lanes L-01 (task/T-01: a lane row naming T-01 and base main,
// whose stored spec unlocks no protected file) and S-01 (detached); protected src/core/types.ts.
type fx struct {
	*testkit.Project
	P, L, S string
}

func fixture(t testing.TB) *fx {
	t.Helper()
	p := testkit.New(t)
	f := &fx{Project: p, P: p.Root, L: p.Lane("L-01", "task/T-01"), S: p.Lane("S-01", "")}
	f.Config(testkit.Set("protected_files", `["src/core/types.ts"]`))
	for _, d := range []string{f.P + "/src/core", f.L + "/src/core", f.L + "/src/a"} {
		os.MkdirAll(d, 0o755)
	}
	f.spec()
	f.State("L-01", `{"lane":"L-01","task":"T-01","branch":"task/T-01","base":"main","blocks":0}`)
	return f
}

// spec stores T-01's spec (obor spec set, T-440: the next rev) with these lines as its "Protected files this task may
// edit" section.
func (f *fx) spec(lines ...string) {
	f.Spec("T-01", "# T-01\n\n## Do\nThings in `src/core/types.ts`.\n\n## Protected files this task may edit\n"+
		strings.Join(lines, "\n")+"\n\n## Tests\n`tests/other.sh`\n")
}

// tc is one hook call: who = "ROLE [AGENT]" ("-" = OBOR_ROLE unset), the tool's cwd, tool and argument (Bash command
// or file path), want = "" to allow, else a substring of the deny reason.
type tc struct {
	who, cwd, tool, arg, want string
}

func bash(who, cwd, cmd, want string) tc     { return tc{who, cwd, "Bash", cmd, want} }
func edit(who, cwd, tool, p, want string) tc { return tc{who, cwd, tool, p, want} }

func (c tc) input(extra map[string]any) []byte {
	in := map[string]any{"hook_event_name": "PreToolUse", "tool_name": c.tool, "cwd": c.cwd}
	switch c.tool {
	case "Bash":
		in["tool_input"] = map[string]any{"command": c.arg, "description": "x"}
	case "NotebookEdit":
		in["tool_input"] = map[string]any{"notebook_path": c.arg}
	default:
		in["tool_input"] = map[string]any{"file_path": c.arg}
	}
	for k, v := range extra {
		in[k] = v
	}
	b, _ := json.Marshal(in)
	return b
}

// runAs runs the guard with OBOR_ROLE / OBOR_AGENT from who and env (more VAR=value) in p's test environment.
func runAs(p *testkit.Project, who string, input []byte, env ...string) (int, string) {
	role, agent, _ := strings.Cut(who, " ")
	if role == "-" {
		role = ""
	}
	return Run(input, p.Env(append([]string{"OBOR_ROLE=" + role, "OBOR_AGENT=" + agent}, env...)...))
}

func check(t *testing.T, p *testkit.Project, cases []tc, env ...string) {
	t.Helper()
	for _, c := range cases {
		code, reason := runAs(p, c.who, c.input(nil), env...)
		where := c.who + " " + c.tool + " [" + c.arg + "] in " + c.cwd
		switch {
		case c.want == "" && code != 0:
			t.Errorf("%s: denied (%s), want allow", where, reason)
		case c.want != "" && code != 2:
			t.Errorf("%s: allowed, want deny %q", where, c.want)
		case c.want != "" && !strings.Contains(reason, c.want):
			t.Errorf("%s: reason %q, want %q", where, reason, c.want)
		case strings.Contains(reason, "\n"):
			t.Errorf("%s: reason has a newline: %q", where, reason)
		}
	}
}

func TestMain(m *testing.M) { testkit.Main(m) }

// bash case_git_push
func TestGitPushDeniedInEveryFormLookAlikesAllowed(t *testing.T) {
	t.Parallel()
	f := fixture(t)
	P, L := f.P, f.L
	check(t, f.Project, []tc{
		bash("-", P, "git push", "git push is blocked"),
		bash("-", P, "git push origin main", "git push"),
		bash("lead", P, "git push", "git push"),
		bash("integrator", P, "git -C "+L+" push --force", "git push"),
		bash("-", P, "git -c user.name=x --no-pager push", "git push"),
		bash("-", P, "/usr/bin/git push", "git push"),
		bash("-", P, "env FOO=1 git push", "git push"),
		bash("-", P, "FOO=1 BAR=2 git push", "git push"),
		bash("-", P, "sudo -u me command time git push", "git push"),
		bash("-", P, "bash -c 'git push'", "git push"),
		bash("-", P, `sh -c "cd x && git push"`, "git push"),
		bash("-", P, "bash -lc 'echo hi; git push'", "git push"),
		bash("-", P, "git status && git push", "git push"),
		bash("-", P, "git status; git push", "git push"),
		bash("-", P, "false || git push", "git push"),
		bash("-", P, "echo y | git push", "git push"),
		bash("-", P, "echo a\ngit push", "git push"),
		bash("-", P, "(cd sub && git push)", "git push"),
		bash("-", P, "FOO=1 env BAR=2 sudo -u me command time /usr/bin/git push", "git push"),
		bash("-", P, "git log --format=push", ""),
		bash("-", P, "git log --grep push", ""),
		bash("-", P, "echo 'git push'", ""),
		bash("-", P, `echo "git push" > notes.txt`, ""),
		bash("-", P, "grep -r 'git push' docs", ""),
		bash("-", P, "ls # git push later", ""),
		bash("-", P, "cat > notes.md <<'EOF'\ngit push\npkill -f x\nEOF\necho done", ""),
		bash("-", P, "cat <<-EOF\n\tgit push\n\tEOF", ""),
	})
}

// bash case_allow_push (T-105, T-786): an entry is a role ("lead") or a role's derived name ("pj-lead", read as its
// slot); the session's role is what counts, never the name in a (stale) OBOR_AGENT.
func TestAllowPushListedNonWorkerOnly(t *testing.T) {
	t.Parallel()
	f := fixture(t)
	P, L := f.P, f.L
	for _, list := range []string{`["lead", "worker"]`, `["pj-lead", "pj-l-01"]`} {
		f.Config(testkit.Set("protected_files", `["src/core/types.ts"]`), testkit.Set("guards.allow_push", list))
		check(t, f.Project, []tc{
			bash("lead", P, "git push", ""),
			bash("lead", P, "git -C "+L+" push origin main", ""),
			bash("lead", P, "git -c user.name=x push", ""),
			bash("lead", P, "gh repo create r --push", ""),
			bash("lead", P, "gh pr create --title t", ""),
			bash("lead", P, "gh repo sync owner/repo", ""),
			bash("lead pj-old-lead", P, "git push", ""), // a stale OBOR_AGENT changes nothing
			// Listed, but a worker: OBOR_ROLE=worker, or a lane cwd (even with a non-worker OBOR_ROLE).
			bash("worker", P, "git push", "guards.allow_push"),
			bash("worker pj-lead", P, "gh pr create", "guards.allow_push"),
			bash("-", L, "git push", "guards.allow_push"),
			bash("lead", L, "gh repo create r --push", "guards.allow_push"),
			bash("- pj-l-01", L, "git push", "guards.allow_push"),
			// An unlisted role, or no role: an OBOR_AGENT naming the lead is no identity.
			bash("integrator", P, "git push", "guards.allow_push"),
			bash("integrator pj-lead", P, "git -C x push", "guards.allow_push"),
			bash("reviewer", P, "gh repo create r --push", "gh repo create --push"),
			bash("reviewer", P, "gh pr create", "gh pr create (pushes the branch)"),
			bash("reviewer", P, "gh pr create --head b --base main", "gh pr create"),
			bash("reviewer", P, "gh repo sync owner/repo", "gh repo sync <repo>"),
			bash("reviewer", P, "gh repo sync -b main owner/repo", "gh repo sync"),
			bash("reviewer", P, "bash -c 'gh pr create'", "gh pr create"),
			bash("- pj-lead", P, "git push", "guards.allow_push"),
			bash("-", P, "gh pr create", "guards.allow_push"),
			// Not pushes.
			bash("reviewer", P, "gh repo create r", ""),
			bash("reviewer", P, "gh repo sync", ""),
			bash("reviewer", P, "gh repo sync -b main", ""),
			bash("reviewer", P, "gh pr list", ""),
			bash("reviewer", P, "gh pr view create", ""),
			bash("reviewer", P, "echo gh pr create", ""),
			bash("reviewer", P, "gh", ""),
		})
	}
	// No guards.allow_push, or an entry that names no role → everyone denied.
	for _, list := range []string{"", `["pj-boss"]`} {
		edits := []func(map[string]any){testkit.Set("protected_files", `["src/core/types.ts"]`)}
		if list != "" {
			edits = append(edits, testkit.Set("guards.allow_push", list))
		}
		f.Config(edits...)
		check(t, f.Project, []tc{
			bash("lead", P, "git push", "guards.allow_push"),
			bash("lead pj-boss", P, "gh pr create", "guards.allow_push"),
		})
	}
}

// bash case_pkill_killall
func TestPkillKillallDeniedKillPIDAllowed(t *testing.T) {
	t.Parallel()
	f := fixture(t)
	P, L := f.P, f.L
	check(t, f.Project, []tc{
		bash("-", P, "pkill -f x", "pkill kills by pattern"),
		bash("-", P, "pkill node", "pkill"),
		bash("lead", P, "killall bun", "killall kills by pattern"),
		bash("-", P, "sudo pkill x", "pkill"),
		bash("-", P, "sudo pkill -f x", "pkill kills by pattern"),
		bash("-", P, "/usr/bin/pkill -9 x", "pkill"),
		bash("worker", L, "sleep 1; pkill -f 'bun run check'", "by PID (kill <pid>)"),
		bash("-", P, "kill 123", ""),
		bash("-", P, "kill -TERM 123 456", ""),
		bash("-", P, "ps aux | grep pkill", ""),
		bash("-", P, "ps aux | grep pkill; kill 123", ""),
		bash("-", P, "man killall", ""),
	})
}

// T-470: codex exec, claude -p / --print and opencode run are denied for every role (lead included), wrapped or
// nested too; the interactive forms and look-alikes are allowed.
func TestHeadlessAgentRunsDenied(t *testing.T) {
	t.Parallel()
	f := fixture(t)
	P, L := f.P, f.L
	const why = "runs an agent headless; agents run in herdr panes"
	cases := []tc{}
	for _, who := range []string{"lead", "integrator", "worker", "reviewer", "-"} {
		cwd := P
		if who == "worker" {
			cwd = L
		}
		cases = append(cases,
			bash(who, cwd, `codex exec "fix the bug"`, "codex exec "+why),
			bash(who, cwd, `codex -m gpt-6 -s workspace-write exec "x"`, "codex exec"),
			bash(who, cwd, `claude -p "summarise"`, "claude -p "+why),
			bash(who, cwd, `claude --model opus --print "x"`, "claude --print "+why),
			bash(who, cwd, `claude --print=json`, "claude --print"),
			bash(who, cwd, `opencode run "x"`, "opencode run "+why),
			bash(who, cwd, `/usr/local/bin/opencode run x`, "opencode run"),
			bash(who, cwd, `timeout 600 codex exec x`, "codex exec"),
			bash(who, cwd, `sh -c 'claude -p hi'`, "claude -p"),
			bash(who, cwd, `cd /tmp && env FOO=1 opencode run x`, "opencode run"),
			bash(who, cwd, `FOO=bar /opt/bin/codex "exec" "hi"`, "codex exec"),
			bash(who, cwd, `env FOO=bar /opt/bin/claude "--print" "hi"`, "claude --print"),
			bash(who, cwd, `command /opt/bin/opencode 'run' hi`, "opencode run"),
			bash(who, cwd, `env -S 'codex exec hi'`, "codex exec"),
			bash(who, cwd, `env -S 'claude --print hi'`, "claude --print"),
			bash(who, cwd, `env -S 'opencode run hi'`, "opencode run"),
			bash(who, cwd, `claude -cp hi`, "claude -cp"),
			bash(who, cwd, `claude -pc hi`, "claude -pc"),
			bash(who, cwd, `codex e hi`, "codex e"),
		)
	}
	cases = append(cases,
		bash("lead", P, "claude", ""),
		bash("lead", P, "claude --resume abc --permission-mode auto", ""),
		bash("lead", P, "codex -m gpt-6", ""),
		bash("lead", P, `codex "please exec the plan"`, ""),
		bash("lead", P, "opencode", ""),
		bash("lead", P, "claude --version", ""),
		bash("lead", P, "which claude codex opencode", ""),
		bash("lead", P, `echo "claude -p x"`, ""),
		bash("lead", P, "grep -r 'codex exec' docs", ""),
		bash("lead", P, `claude -c "fix -p"`, ""),
		bash("lead", P, `claude -- -p`, ""),
		bash("lead", P, `claude -- --print`, ""),
		bash("lead", P, `codex review e`, ""),
		bash("lead", P, `env -S 'claude --resume x'`, ""),
		bash("lead", P, `claude -c --model opus`, ""),
	)
	check(t, f.Project, cases)
}

// T-476 (RV-50 #4): headless is decided by argv structure: codex/opencode's subcommand is the first word that is
// not an option or an option's value; words after `--` never count; env -S strings split with quote removal.
func TestHeadlessByArgvStructure(t *testing.T) {
	t.Parallel()
	f := fixture(t)
	P := f.P
	check(t, f.Project, []tc{
		// deny: the subcommand after global options that take a value
		bash("lead", P, `codex --model x e hi`, "codex e runs an agent headless"),
		bash("lead", P, `codex -m x e hi`, "codex e"),
		bash("lead", P, `codex -c k=v exec hi`, "codex exec"),
		bash("lead", P, `codex -mx exec hi`, "codex exec"),
		bash("lead", P, `codex --model=x exec hi`, "codex exec"),
		bash("lead", P, `codex --oss -C /tmp exec hi`, "codex exec"),
		bash("lead", P, `opencode --session abc run hi`, "opencode run"),
		bash("lead", P, `opencode -s abc run hi`, "opencode run"),
		// RV-51 #1 (owner/lead 2026-10-28): exec / run deny anywhere before --, e only as the subcommand
		bash("lead", P, `codex "hello" exec hi`, "codex exec"),
		bash("lead", P, `codex resume exec`, "codex exec"),
		bash("lead", P, `opencode "hello" run hi`, "opencode run"),
		// accepted, not fixed: the alias after a prompt word; env -S's \_ separator
		bash("lead", P, `codex "hello" e hi`, ""),
		bash("lead", P, `env -S 'codex\_exec hi'`, ""),
		bash("lead", P, `env -S 'claude\_--print hi'`, ""),
		bash("lead", P, `env -S 'opencode\_run hi'`, ""),
		// allow: e / exec as another subcommand's argument, an option's value or after --
		bash("lead", P, `codex review e`, ""),
		bash("lead", P, `codex -m e`, ""),
		bash("lead", P, `codex -m exec`, ""),
		bash("lead", P, `codex -- exec`, ""),
		bash("lead", P, `opencode --prompt run`, ""),
		bash("lead", P, `opencode -- run`, ""),
		// deny: env -S strings split as env splits them (quotes removed), also for git/pkill/killall
		bash("lead", P, `env -S 'codex "exec" hi'`, "codex exec"),
		bash("lead", P, `env -S 'claude "--print" hi'`, "claude --print"),
		bash("lead", P, `env -S 'opencode "run" hi'`, "opencode run"),
		bash("lead", P, `env -S '"/opt/bin/codex" exec hi'`, "codex exec"),
		bash("lead", P, `env -S 'git "push"'`, "git push is blocked"),
		bash("lead", P, `env -S 'pkill "x"'`, "pkill kills by pattern"),
		bash("lead", P, `env -S '"killall" x'`, "killall kills by pattern"),
		bash("lead", P, `env '-Scodex exec hi'`, "codex exec"),
		bash("lead", P, `env '--split-string=claude -p hi'`, "claude -p"),
		// allow: claude's -p inside a value word or after --
		bash("lead", P, `claude -c "fix -p"`, ""),
		bash("lead", P, `claude -- -p`, ""),
		bash("lead", P, `env -S 'claude "fix -p"'`, ""),
		bash("lead", P, `env -S 'codex "please exec"'`, ""),
	})
}

// kitDenyCommands is obor-kit's guards.deny_commands (testdata/deny-commands.json, the two entries its
// .obor/config.json carries): T-543's Go load-run rule now in config — the flag forms that run the tests many times
// (alone) and two test commands at once.
func kitDenyCommands(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("testdata/deny-commands.json")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// T-905: guards.deny_commands carries what T-543's Go load-run rule hard-coded. obor-kit's two rules deny the commands
// the old rule denied, with the same messages; a config that sets a rule of its own (a bun project's) denies it there,
// a config with no rules denies nothing, and a rule without Alone denies only two matching commands at once.
func TestDenyCommands(t *testing.T) {
	t.Parallel()
	const one = "one plain `go test <pkgs>` run"
	const many = "two or more test commands at once"

	// goCases is the case_load_test_runs table, now config-driven: the same commands are denied with the same message.
	// Known over-denials (a regex sees the resolved words: quotes removed, no Go flag grammar, no last-value-wins): a
	// quoted value that spells a load flag (go test -run '-race', -bench '-count=3', -timeout '-race',
	// -args -race -count=3, ./app.test -test.run '-test.race'), and a later flag that turns one off
	// (go test -race -race=false, go test -count=3 -count=1). They are not asserted here.
	goCases := func(P, L string) []tc {
		cases := []tc{}
		for _, who := range []string{"lead", "integrator", "worker", "reviewer", "-"} {
			cwd := P
			if who == "worker" {
				cwd = L
			}
			cases = append(cases,
				// a load flag on go test, alone and in every shell form the guard walks.
				bash(who, cwd, "go test -count=3 ./...", one),
				bash(who, cwd, "go test -count 3 ./...", one),
				bash(who, cwd, "go test ./internal/app -count=2", one),
				bash(who, cwd, "go test -count=20 -run X ./pkg", one),
				bash(who, cwd, "go test -count=1 -count=3 ./...", one),
				bash(who, cwd, "go test -race ./...", one),
				bash(who, cwd, "go test -race=true ./...", one),
				bash(who, cwd, "go test -race -count=2 ./...", one),
				bash(who, cwd, "go test -test.count=5 ./...", one),
				// Go's flag package takes --name too, raceOn denied any value but false/0, and goSub skipped leading flags.
				bash(who, cwd, "go test --count=3 ./...", one),
				bash(who, cwd, "go test --count 3 ./...", one),
				bash(who, cwd, "go test --race ./...", one),
				bash(who, cwd, "go test -race=t ./...", one),
				bash(who, cwd, "go test -race=T ./...", one),
				bash(who, cwd, "go test -race=TRUE ./...", one),
				bash(who, cwd, "go test -race=1 ./...", one),
				bash(who, cwd, "go test --test.count=5 ./...", one),
				bash(who, cwd, "go test --test.race ./...", one),
				bash(who, cwd, "go test -test.race=t ./...", one),
				bash(who, cwd, "go -C internal test -count=3 ./...", one),
				bash(who, cwd, "go -C=internal test -count=3 ./...", one),
				bash(who, cwd, "go -v test -race ./...", one),
				bash(who, cwd, "cd internal && go test -count=3 ./...", one),
				bash(who, cwd, "go build ./... && go test -count=3 ./...", one),
				bash(who, cwd, "true; go test -race ./pkg", one),
				bash(who, cwd, "echo x | go test -count=2 ./pkg", one),
				bash(who, cwd, "(go test -count=3 ./pkg)", one),
				bash(who, cwd, `echo "$(go test -race ./pkg)"`, one),
				bash(who, cwd, "bash -c 'go test -count=3 ./pkg'", one),
				bash(who, cwd, `sh -c "cd x && go test -race ./pkg"`, one),
				bash(who, cwd, "timeout 600 go test -count=5 ./pkg", one),
				bash(who, cwd, "sudo go test -race ./pkg", one),
				// a compiled test binary.
				bash(who, cwd, "./app.test -test.count=3", one),
				bash(who, cwd, "go test -c -o app.test ./pkg && ./app.test -test.count=20", one),
				bash(who, cwd, "./pkg/app.test -test.race", one),
				bash(who, cwd, "./pkg/app.test -test.v -test.count 5 -test.timeout=1m", one),
				bash(who, cwd, "env FOO=1 ./pkg/app.test -test.count=3", one),
				bash(who, cwd, "bash -c './app.test -test.race'", one),
				bash(who, cwd, "./app.test --test.count=3", one),
				bash(who, cwd, "./app.test -test.race=t", one),
				// two or more test commands at once.
				bash(who, cwd, "go test ./a & go test ./b &", many),
				bash(who, cwd, "go test ./a & ./app.test &", many),
				bash(who, cwd, "bash -c 'go test ./a & go test ./b &'", many),
				bash(who, cwd, "(go test ./a & go test ./b &)", many),
				bash(who, cwd, `printf '%s\n' ./a ./b | xargs -P 4 -I{} go test {}`, many),
				bash(who, cwd, "ls pkgs | xargs -P4 go test", many),
				bash(who, cwd, "xargs --max-procs=4 go test", many),
				bash(who, cwd, "xargs --max-procs 4 go test", many),
				bash(who, cwd, "parallel -j 4 go test ::: ./a ./b", many),
			)
		}
		return append(cases,
			// allowed: one plain run, -count=1 (cache off), and the one-at-a-time forms.
			bash("-", P, "go test ./...", ""),
			bash("-", P, "go test -count=1 ./internal/app", ""),
			bash("-", P, "go test -run X ./pkg", ""),
			bash("-", P, "go test -count=1 -run X -timeout 10m ./...", ""),
			bash("-", P, "go test -race=false ./...", ""),
			bash("-", P, "go test -race=0 ./...", ""),
			bash("-", P, "go test --count=1 ./...", ""),
			bash("-", P, "go -C internal test ./...", ""),
			bash("-", P, "go -v test ./...", ""),
			bash("-", P, "go test -count=0 ./...", ""),
			bash("-", P, "go test -tags race ./...", ""),
			bash("-", P, "go test ./a; go test ./b", ""), // the rule is the background / parallel forms only
			bash("-", P, "go test ./a & go test ./b", ""),
			bash("-", P, "go test ./a & git status && go test ./b", ""),
			bash("-", P, "go test ./a; go test ./b & wait", ""),
			bash("-", P, "(go test ./a && go test ./b) & wait", ""),
			bash("-", P, "go test ./a & wait; go test ./b & wait", ""),
			bash("-", P, "if true; then go test ./a & else go test ./b & fi", ""),
			bash("-", P, "go test ./... &", ""), // a single run is not two or more
			bash("-", P, "go build ./... &", ""),
			bash("-", P, "go build ./... & go build ./... & wait", ""),
			bash("-", P, "./app.test -test.count=1", ""),
			bash("-", P, "./app.test -test.v", ""),
			bash("-", P, "xargs -P 4 go build", ""),
			bash("-", P, "xargs go test ./...", ""), // no -P: one at a time
			bash("-", P, "xargs -P1 go test ./...", ""),
			bash("-", P, "xargs -P 1 go test ./...", ""),
			bash("-", P, "xargs --max-procs=1 go test ./...", ""),
			bash("-", P, "parallel -j 1 go test ./...", ""),
			bash("-", P, "parallel -j1 go test ./...", ""),
			bash("-", P, "parallel --jobs=1 go test ./...", ""),
			bash("-", P, "parallel go test ./...", ""),
			bash("-", P, "parallel -j 4 go build ::: ./a ./b", ""),
			bash("-", P, "go vet -race ./...", ""),
			bash("-", P, "echo 'go test -count=3 ./...'", ""),
			bash("-", P, "grep -r 'go test -race' docs", ""),
			bash("-", P, "cat > notes.md <<'EOF'\ngo test -count=3\ngo test ./a & go test ./b\nEOF", ""),
		)
	}

	bunRule := `[{"match": "^bun test --coverage", "alone": true, "message": "a coverage run is a load run — one plain bun test run"}]`
	pairsRule := `[{"match": "^bun test", "message": "two bun test commands at once are a load run"}]`
	for _, c := range []struct {
		name  string
		rules string
		cases func(P, L string) []tc
	}{
		{"obor-kit's Go rules", kitDenyCommands(t), goCases},
		{"a bun project's rule", bunRule, func(P, _ string) []tc {
			return []tc{
				bash("-", P, "bun test --coverage", "a coverage run is a load run"),
				bash("-", P, "bun test", ""),
				bash("-", P, "bun build", ""),
			}
		}},
		{"a rule without alone denies only two at once", pairsRule, func(P, _ string) []tc {
			return []tc{
				bash("-", P, "bun test", ""),
				bash("-", P, "bun test ./a; bun test ./b", ""),
				bash("-", P, "bun test ./a & bun test ./b &", "two bun test commands at once"),
				bash("-", P, "ls pkgs | xargs -P4 bun test", "two bun test commands at once"),
			}
		}},
		{"no rules deny nothing", `[]`, func(P, _ string) []tc {
			return []tc{
				bash("-", P, "go test -count=3 ./...", ""),
				bash("-", P, "./app.test -test.race", ""),
				bash("-", P, "go test ./a & go test ./b &", ""),
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := fixture(t)
			f.Config(testkit.Set("guards.deny_commands", c.rules))
			check(t, f.Project, c.cases(f.P, f.L))
		})
	}
}

// bash case_commit_all_in_main_vs_lane
func TestCommitAllDeniedInMainAllowedInALane(t *testing.T) {
	t.Parallel()
	f := fixture(t)
	P, L := f.P, f.L
	check(t, f.Project, []tc{
		bash("integrator", P, "git commit -am wip", "git commit -a in the main checkout"),
		bash("integrator", P, "git commit --all -m wip", "commit -a"),
		bash("integrator", P, "git commit -qa -m wip", "commit -a"),
		bash("lead", P, "git commit -a", "commit -a"),
		bash("-", P+"/src", "git commit -am wip", "commit -a"),
		bash("integrator", L, "git -C "+P+" commit -am wip", "commit -a"),
		bash("integrator", P, "cd "+L+" && cd "+P+" && git commit -am wip", "commit -a"),
		bash("integrator", P, "git -C "+L+" commit -am wip", ""),
		bash("integrator", P, "git -C ../proj-wt/L-01 commit -am wip", ""),
		bash("integrator", P, "cd "+L+" && git commit -am wip", ""),
		bash("integrator", P, "git commit -m 'add a thing'", ""),
		bash("integrator", P, "git commit -m -a", ""),
		bash("integrator", P, "git commit -F msg.txt", ""),
		bash("integrator", P, "git add -A && git commit -m x", ""),
		bash("integrator", P, "cd nonexistent && git commit -am x", ""),
		bash("lead", P, "git commit --amend --no-edit", ""),
	})
}

// bash case_worker_git_writes
func TestWorkerGitWritesDeniedReadsAllowed(t *testing.T) {
	t.Parallel()
	f := fixture(t)
	P, L := f.P, f.L
	var cases []tc
	for _, sub := range []string{"commit -m x", "merge main", "rebase main", "reset --hard", "reset HEAD~1", "checkout -b x",
		"checkout main", "switch main", "restore src/a/x", "stash", "stash pop", "cherry-pick abc", "revert HEAD", "tag v1",
		"worktree add ../x", "clean -fd", "am x.patch", "apply x.patch"} {
		cases = append(cases, bash("-", L, "git "+sub, "workers don't run git "+strings.Fields(sub)[0]))
	}
	for _, sub := range []string{"status", "diff", "diff --stat main", "log --oneline -3", "show HEAD", "blame README",
		"grep foo", "ls-files", "rev-parse HEAD", "merge-base main HEAD", "branch", "branch -v", "branch --list"} {
		cases = append(cases, bash("-", L, "git "+sub, ""))
	}
	cases = append(cases,
		bash("-", L, "git branch -D old", "workers don't run git branch -D"),
		bash("-", L, "git branch -m new", "git branch"),
		bash("-", L, "git branch --force x main", "git branch --force"),
		bash("-", L+"/src", "git -C "+L+" commit -m x", "workers don't run git commit"),
		bash("worker", P, "git commit -m x", "leave changes uncommitted"),
		bash("integrator", L, "git commit -m x", "workers don't run git commit"),
		bash("integrator", P, "git commit -m x", ""),
		bash("integrator", P, "git merge --no-ff task/T-01", ""), // T-853: the main checkout's HEAD rows went
	)
	check(t, f.Project, cases)
}

// T-853 (was T-841 / T-848's case_main_checkout_head): the main checkout's HEAD rows went — the integrator, the
// reviewer and the deep dive run in their role worktrees, so no role but the lead (and the custom roles, which read git
// only) runs in the main checkout. What stays: a GitReadOnly role runs git reads only, except that the reviewer and the
// deep dive check out any commit in their own role worktree (never a branch they create, never a path, never another
// worktree); a non-lead in a lane or role worktree may check out (the integrator a candidate in its own).
func TestRoleCheckoutRows(t *testing.T) {
	t.Parallel()
	f := fixture(t)
	P := f.P
	scratch := f.LaneDir("merge")
	rev, deep, integ := f.LaneDir("reviewer"), f.LaneDir("deep_dive"), f.LaneDir("integrator")
	for _, d := range []string{scratch, rev, deep, integ} {
		f.Git(P, "worktree", "add", "-q", "--detach", d, "main")
	}
	sha := strings.TrimSpace(f.Git(P, "rev-parse", "main"))
	const readOnly = "it runs git reads only"
	verbs := []string{"checkout main", "switch main", "reset --hard", "rebase main", "merge main", "stash",
		"cherry-pick abc", "revert HEAD", "am x.patch", "apply x.patch", "clean -fd"}
	var cases []tc
	// The rows that went: a non-lead that is not read-only no longer meets a main-checkout HEAD row, here or with
	// -C / --git-dir / --work-tree / GIT_DIR from the scratch, --detach (T-848) included.
	for _, v := range verbs {
		cases = append(cases,
			bash("integrator", P, "git "+v, ""),
			bash("integrator", scratch, "git -C "+P+" "+v, ""),
			bash("integrator", scratch, "git "+v, ""),
			bash("lead", P, "git "+v, ""))
	}
	cases = append(cases,
		bash("integrator", scratch, "git --git-dir="+P+"/.git reset --hard", ""),
		bash("integrator", scratch, "git --work-tree="+P+" switch main", ""),
		bash("integrator", scratch, "GIT_DIR="+P+"/.git git checkout --detach", ""),
		bash("integrator", P, "git checkout --detach", ""),
		bash("integrator", P, "git checkout -b wip", ""),
		// The rows that stay: a path checkout keeps the checkWrite rule; a worker runs no git write.
		bash("integrator", P, "git checkout -- src/core/types.ts", "src/core/types.ts is a protected file"),
		bash("integrator", P, "git checkout main -- src/a/x.ts", ""),
		bash("worker", f.L, "git checkout main", "workers don't run git checkout"),
		// A non-lead in a lane may check out: the owner's drive agent; the integrator a candidate in its role worktree.
		bash("driver pj-l-01-drive", f.L, "git checkout -q main", ""),
		bash("integrator", integ, "git checkout -q --detach "+sha, ""),
		bash("integrator", integ, "git checkout "+sha, ""),
		// The reviewer and the deep dive check out any commit in their own role worktree.
		bash("reviewer", rev, "git checkout "+sha, ""),
		bash("reviewer", rev, "git checkout --detach main", ""),
		bash("reviewer", rev, "git checkout -q -d "+sha, ""),
		bash("reviewer", rev, "git switch --detach main", ""),
		bash("reviewer", rev+"/src", "git -C "+rev+" checkout "+sha, ""),
		bash("reviewer", P, "git -C "+rev+" checkout "+sha, ""),
		bash("deep_dive", deep, "git checkout "+sha, ""),
		// … but no branch of their own, no path, no other git write, and not in another worktree.
		bash("reviewer", rev, "git checkout -b wip", readOnly),
		bash("reviewer", rev, "git switch -c wip", readOnly),
		bash("reviewer", rev, "git checkout --orphan=wip", readOnly),
		bash("reviewer", rev, "git checkout -- .gitignore", readOnly),
		bash("reviewer", rev, "git checkout main -- .gitignore", readOnly),
		bash("reviewer", rev, "git reset --hard", readOnly),
		bash("reviewer", rev, "git -C "+P+" checkout main", readOnly),
		bash("reviewer", P, "git checkout main", readOnly),
		bash("reviewer", deep, "git checkout "+sha, readOnly),
		bash("deep_dive", rev, "git checkout "+sha, readOnly),
		bash("advisor", P, "git checkout main", readOnly),
		bash("advisor", rev, "git checkout "+sha, readOnly),
	)
	// Reads stay allowed everywhere.
	for _, who := range []string{"integrator", "reviewer", "advisor"} {
		for _, dir := range []string{P, scratch, rev} {
			cases = append(cases,
				bash(who, dir, "git show HEAD:.gitignore", ""),
				bash(who, dir, "git log --oneline -3", ""))
		}
	}
	check(t, f.Project, cases)
}

// T-212: index writes are denied for workers only.
func TestWorkerIndexWritesDeniedLeadAndIntegratorAllowed(t *testing.T) {
	t.Parallel()
	f := fixture(t)
	P, L := f.P, f.L
	check(t, f.Project, []tc{
		bash("-", L, "git add f", "workers don't run git add"),
		bash("-", L, "git add -N .", "workers don't run git add"),
		bash("-", L, "git add --intent-to-add src", "workers don't run git add"),
		bash("-", L+"/src", "git -C "+L+" add f", "workers don't run git add"),
		bash("-", L, "git rm f", "workers don't run git rm"),
		bash("-", L, "git mv a b", "workers don't run git mv"),
		bash("-", L, "git status", ""),
		bash("-", L, "git diff", ""),
		bash("lead", P, "git add f", ""),
		bash("integrator", P, "git add f", ""),
		bash("lead", P, "$X rm file", ""),
		bash("lead", P, "$X add file", ""),
		bash("integrator", P, "$X mv a b", ""),
	})
}

// link creates a symlink at path to target.
func link(t *testing.T, path, target string) {
	t.Helper()
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
}

// RV-40 #5 (T-372), Extent "checked by probe": all four file-tool entry points deny a lane symlink whose destination is a
// sibling worktree (the lane path alone would pass); the neighbouring checks keep their results.
func TestRV40LeafSymlinkIntoAnotherWorktree(t *testing.T) {
	t.Parallel()
	f := fixture(t)
	P, L, S := f.P, f.L, f.S
	foreign := S + "/src/a/foreign.txt"
	f.Write(foreign, "other lane\n")
	link(t, L+"/src/a/link.ts", foreign)
	link(t, L+"/src/link.ts", foreign)
	want := "is a symlink to " + foreign + " in another worktree of this repo (" + S + ")"
	var cases []tc
	for _, tool := range []string{"Edit", "Write", "MultiEdit", "NotebookEdit"} {
		cases = append(cases,
			edit("-", L, tool, L+"/src/a/link.ts", want),
			edit("-", L, tool, L+"/src/link.ts", want))
	}
	check(t, f.Project, cases)
	// The same fixture's other rules: a direct other-worktree path, a protected redirection through a link, and a
	// worker Git write; and a regular protected file the spec names.
	f.Write(P+"/src/core/types.ts", "protected\n")
	link(t, L+"/protected.ts", P+"/src/core/types.ts")
	f.spec("`src/core/types.ts`")
	check(t, f.Project, []tc{
		edit("-", L, "Write", P+"/STATUS.md", "is in another worktree of this repo ("+P+")"),
		bash("-", L, "echo x > protected.ts", "src/core/types.ts is a protected file"),
		bash("-", L, "git commit -m x", "workers don't run git commit"),
	})
	f.Write(L+"/src/core/types.ts", "regular\n")
	check(t, f.Project, []tc{
		edit("-", L, "Edit", L+"/src/core/types.ts", ""),
		edit("-", L, "Write", L+"/src/core/types.ts", ""),
	})
}

// RV-40 #5 (T-372), Extent "checked by code": a leaf link into the main checkout or a role worktree shares the path; a
// spec naming the link as a protected unlock does not turn the destination check off.
func TestRV40LeafSymlinkIntoMainAndRoleWorktree(t *testing.T) {
	t.Parallel()
	f := fixture(t)
	P, L := f.P, f.L
	role := f.Base + "/review"
	f.Git(P, "worktree", "add", "-q", "--detach", role, "main")
	f.Write(role+"/STATUS.md", "role worktree\n")
	link(t, L+"/src/a/main.ts", P+"/STATUS.md")
	link(t, L+"/src/a/role.ts", role+"/STATUS.md")
	check(t, f.Project, []tc{
		edit("-", L, "Edit", L+"/src/a/main.ts", "is a symlink to "+P+"/STATUS.md in another worktree of this repo ("+P+")"),
		edit("-", L, "Write", L+"/src/a/role.ts", "is a symlink to "+role+"/STATUS.md in another worktree of this repo ("+role+")"),
	})
	f.spec("`src/a/main.ts`", "`src/a/role.ts`") // an exact unlock entry, as in the T-238 unlock
	check(t, f.Project, []tc{
		edit("-", L, "Edit", L+"/src/a/main.ts", "in another worktree"),
		edit("-", L, "Edit", L+"/src/a/role.ts", "in another worktree"),
	})
}

// RV-40 #5 (T-372), Extent "unchecked by dedicated probe": the control shapes — a directory symlink is already resolved by
// phys, a protected link target keeps its protected deny, and a dangling link is judged by its target path.
func TestRV40LeafSymlinkControls(t *testing.T) {
	t.Parallel()
	f := fixture(t)
	P, L, S := f.P, f.L, f.S
	os.MkdirAll(S+"/src/a", 0o755)
	link(t, L+"/src/a/dir", S+"/src/a")
	check(t, f.Project, []tc{
		edit("-", L, "Write", L+"/src/a/dir/x.ts", "is in another worktree of this repo ("+S+")"),
	})
	f.Write(P+"/src/core/types.ts", "protected\n")
	link(t, L+"/src/core/types-link.ts", P+"/src/core/types.ts")
	check(t, f.Project, []tc{
		edit("-", L, "Edit", L+"/src/core/types-link.ts", "src/core/types.ts is a protected file"),
	})
	// Dangling, destination inside the lane → allowed.
	link(t, L+"/src/a/dangle.ts", L+"/src/a/missing.ts")
	// Dangling, destination in another lane or the main checkout → denied by the destination.
	link(t, L+"/src/a/out.ts", S+"/src/a/missing.ts")
	link(t, L+"/src/a/main-dangle.ts", P+"/missing.ts")
	// A chain through an in-lane hop is followed to its destination.
	f.Write(S+"/src/a/foreign.txt", "other lane\n")
	link(t, L+"/src/a/hop.ts", S+"/src/a/foreign.txt")
	link(t, L+"/src/a/chain.ts", L+"/src/a/hop.ts")
	check(t, f.Project, []tc{
		edit("-", L, "Edit", L+"/src/a/dangle.ts", ""),
		edit("-", L, "Edit", L+"/src/a/out.ts", "is a symlink to "+S+"/src/a/missing.ts in another worktree of this repo ("+S+")"),
		edit("-", L, "Edit", L+"/src/a/main-dangle.ts", "is a symlink to "+P+"/missing.ts in another worktree of this repo ("+P+")"),
		edit("-", L, "Edit", L+"/src/a/chain.ts", "is a symlink to "+S+"/src/a/foreign.txt in another worktree"),
	})
}

// bash case_worker_edit; T-496: no per-file guard: a worker edits any file of its own lane (no spec list, no lane row,
// no stored spec needed); another lane, the main checkout and another obor project are denied; a protected file is
// denied unless the spec names it; git writes stay denied. Paths in no obor project (scratch, /tmp) are not the guard's.
func TestWorkerEditsAnywhereInItsLaneOnly(t *testing.T) {
	t.Parallel()
	f := fixture(t)
	P, L, S := f.P, f.L, f.S
	other := testkit.New(t) // another obor project
	other.Config()
	os.MkdirAll(f.Base+"/outside", 0o755)
	if !project.IsDir(other.Root + "/.obor") {
		t.Fatal("precondition: the other project has .obor/")
	}
	inLane := []string{L + "/src/a/x.ts", L + "/src/b.ts", L + "/README.md", L + "/.task-done.md", L + "/new/dir/deep.go"}
	var cases []tc
	for _, tool := range []string{"Edit", "Write", "MultiEdit", "NotebookEdit"} {
		for _, p := range inLane {
			cases = append(cases, edit("-", L, tool, p, ""))
		}
		cases = append(cases,
			edit("-", L, tool, S+"/src/a/x.ts", "is in another worktree of this repo ("+S+") — a worker edits only inside its lane "+L),
			edit("-", L, tool, P+"/README.md", "is in another worktree of this repo ("+P+") — a worker edits only inside its lane "+L),
			edit("-", L, tool, other.Root+"/README.md", "is in another obor project ("+other.Root+") — a worker edits only inside its lane "+L),
			edit("-", L, tool, other.Root+"/new/x.go", "is in another obor project"),
			edit("-", L, tool, L+"/src/core/types.ts", "src/core/types.ts is a protected file"))
	}
	check(t, f.Project, append(cases,
		edit("-", L+"/src", "Edit", "a/new/deep.ts", ""),
		edit("-", L, "Edit", L+"/../S-01/src/a/x.ts", "another worktree"),
		edit("-", L, "Write", f.Base+"/outside/scratch.txt", ""),
		edit("-", L, "Write", "/tmp/obor-pre-tool-use-test-nonexistent/x.txt", ""),
		bash("-", L, "git commit -m x", "workers don't run git commit"),
		bash("-", L, "git add -A", "worker"),
		bash("-", L, "echo x > src/b.ts", ""),
		// Non-workers aren't confined to a lane; the reviewer write profile denies README.md.
		edit("integrator", P, "Edit", L+"/src/b.ts", ""),
		edit("integrator", P, "Edit", other.Root+"/README.md", ""),
		edit("reviewer", P, "Edit", P+"/README.md", "outside the reviewer write profile"),
	))
	// OBOR_ROLE=worker + OBOR_LANE from the main checkout: the lane is still lanes_dir/L-01.
	check(t, f.Project, []tc{
		edit("worker", P, "Edit", L+"/src/b.ts", ""),
		edit("worker", P, "Edit", S+"/src/b.ts", "another worktree"),
	}, "OBOR_LANE=L-01")
	// No stored spec for the task, a row naming no task, no row: the same answers.
	for _, state := range []string{`{"lane":"L-01","task":"T-09","branch":"task/T-09","base":"main","blocks":0}`,
		`{"lane":"L-01","task":"","base":"main"}`, ""} {
		if state == "" {
			f.ClearState("L-01")
		} else {
			f.State("L-01", state)
		}
		check(t, f.Project, []tc{
			edit("-", L, "Edit", L+"/src/b.ts", ""),
			edit("-", L, "Edit", S+"/src/b.ts", "another worktree"),
			edit("-", L, "Edit", L+"/src/core/types.ts", "protected"),
		})
	}
}

// T-496 (was T-439, T-478): a store that cannot be read, or a lanes/specs table missing, no longer denies lane edits
// (there is no files list to arm); it only means the spec's protected unlocks cannot be read: the protected file stays
// denied (fail closed), for Edit and Bash writes alike. (A newer schema whose tables still read is read as it is.)
func TestStoreUnreadableKeepsProtectedLocked(t *testing.T) {
	exec := func(q string) func(f *fx) {
		return func(f *fx) {
			db, err := store.OpenRW(store.File(f.P))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.Exec(q); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, c := range []struct {
		name  string
		setup func(f *fx)
	}{
		{"not a database", func(f *fx) { f.Write(".obor/state/obor.db", "not a database\n") }},
		{"missing lanes table", exec("DROP TABLE lanes")},
		{"missing specs table", exec("DROP TABLE specs")},
		{"missing store", func(f *fx) { os.Remove(store.File(f.P)) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := fixture(t)
			P, L := f.P, f.L
			f.spec("`src/core/types.ts`")
			check(t, f.Project, []tc{edit("-", L, "Edit", L+"/src/core/types.ts", "")}) // precondition: unlocked
			c.setup(f)
			check(t, f.Project, []tc{
				edit("-", L, "Edit", L+"/src/core/types.ts", "is a protected file"),
				bash("-", L, "echo x > src/core/types.ts", "is a protected file"),
				edit("-", L, "Edit", L+"/src/b.ts", ""),
				bash("-", L, "echo x > src/b.ts", ""),
				edit("-", L, "Edit", P+"/README.md", "is in another worktree of this repo"),
				edit("integrator", P, "Edit", P+"/README.md", ""),
				edit("lead", P, "Edit", P+"/src/core/types.ts", ""),
			})
		})
	}
}

// gitShows puts a git wrapper first on PATH that counts `git show` calls; returns the count reader.
func gitShows(t *testing.T, f *fx) func() int {
	t.Helper()
	real := ""
	for _, d := range filepath.SplitList(os.Getenv("PATH")) {
		if fi, err := os.Stat(d + "/git"); err == nil && !fi.IsDir() {
			real = d + "/git"
			break
		}
	}
	log := f.Base + "/shows"
	f.Write(f.Base+"/gitbin/git", "#!/bin/sh\nfor a in \"$@\"; do [ \"$a\" = show ] && echo show >> '"+log+"'; done\nexec '"+real+"' \"$@\"\n")
	os.Chmod(f.Base+"/gitbin/git", 0o755)
	f.Write(log, "")
	t.Setenv("PATH", f.Base+"/gitbin:"+os.Getenv("PATH"))
	return func() int { return strings.Count(f.Read(log), "show") }
}

// bash case_worker_edit_spec_on_base; T-440: the protected unlock reads the stored spec's current rev, never git.
func TestProtectedUnlockReadsTheStoredSpec(t *testing.T) {
	f := fixture(t)
	L := f.L
	check(t, f.Project, []tc{edit("-", L, "Edit", L+"/src/core/types.ts", "is a protected file")})
	// Named in a new rev after dispatch → allowed at the next call, no re-arm.
	f.spec("`src/core/types.ts`")
	check(t, f.Project, []tc{edit("-", L, "Edit", L+"/src/core/types.ts", "")})
	// Removed again → denied.
	f.spec("`src/b.ts` — only this one")
	check(t, f.Project, []tc{edit("-", L, "Edit", L+"/src/core/types.ts", "is a protected file")})
	// A spec in git (committed on main, or the lane's own copy) counts for nothing; no git show at all.
	unlocks := "## Protected files this task may edit\n`src/core/types.ts`\n"
	f.Write("docs/tasks/T-01.md", unlocks)
	f.Git(f.P, "add", "docs/tasks/T-01.md")
	f.Git(f.P, "commit", "-q", "-m", "spec in git")
	f.Write(L+"/docs/tasks/T-01.md", unlocks)
	shows := gitShows(t, f)
	check(t, f.Project, []tc{edit("-", L, "Edit", L+"/src/core/types.ts", "is a protected file"), edit("-", L, "Edit", L+"/src/b.ts", "")})
	if n := shows(); n != 0 {
		t.Errorf("git show calls for two checks: %d, want none", n)
	}
	if files, _ := filepath.Glob(f.P + "/.obor/state/lanes/*.spec-cache"); len(files) != 0 {
		t.Errorf("cache files written: %v", files)
	}
}

// bash case_protected_files
// bash case_protected_files
func TestProtectedFilesDeniedForNonLeads(t *testing.T) {
	t.Parallel()
	f := fixture(t)
	P, L := f.P, f.L
	other := f.Base + "/review"
	f.Git(P, "worktree", "add", "-q", "--detach", other, "main")
	os.MkdirAll(f.Base+"/outside", 0o755)
	f.Write(f.Base+"/outside/types.ts", "")
	check(t, f.Project, []tc{
		edit("-", L, "Edit", L+"/src/core/other.ts", ""),
		edit("-", L, "Edit", L+"/src/core/types.ts", "src/core/types.ts is a protected file (protected_files in .obor/config.json; lead-only) — don't change it; ask the lead"),
		edit("-", L, "NotebookEdit", L+"/src/core/types.ts", "protected"),
		edit("integrator", P, "Edit", P+"/src/core/types.ts", "protected"),
		edit("reviewer", P, "Write", P+"/src/core/types.ts", "protected"),
		edit("advisor", P, "Write", L+"/src/core/types.ts", "protected"),
		edit("lead", P, "Edit", P+"/src/core/types.ts", ""),
		edit("-", P, "Edit", P+"/src/core/types.ts", ""),
		edit("integrator", P, "Edit", P+"/src/core/other.ts", ""),
		// none in a linked worktree that isn't a lane → restricted.
		edit("-", other, "Edit", other+"/src/core/types.ts", "protected"),
		// Bash writes.
		bash("-", L, "sed -i '' 's/a/b/' src/core/types.ts", "src/core/types.ts is a protected file"),
		bash("-", L, "sed -i.bak -e 's/a/b/' src/core/types.ts", "protected"),
		bash("-", L, "sed --in-place 's/a/b/' "+L+"/src/core/types.ts", "protected"),
		bash("-", L, "echo x > src/core/types.ts", "protected"),
		bash("-", L, "echo x>>./src/core/../core/types.ts", "protected"),
		bash("-", L+"/src", "printf x 2> core/types.ts", "protected"),
		bash("-", L, "cat a | tee -a src/core/types.ts", "protected"),
		bash("-", L, "cp /tmp/x src/core/types.ts", "protected"),
		bash("-", L, "mv src/core/types.ts src/core/t2.ts", "protected"),
		bash("-", L, "rm -f src/core/types.ts", "protected"),
		bash("-", L, "cd src && echo x > core/types.ts", "protected"),
		bash("integrator", P, "echo x > src/core/types.ts", "protected"),
		bash("reviewer", P, "cp -t src/core "+f.Base+"/outside/types.ts", "protected"),
		bash("reviewer", P, "bash -c -- 'echo x > src/core/types.ts'", "protected"),
		bash("-", L, "sed 's/a/b/' src/core/types.ts", ""),
		bash("-", L, "sed -n '1,5p' src/core/types.ts > /tmp/obor-out.txt", ""),
		bash("-", L, "sed 's/a/b/' src/core/types.ts; cat src/core/types.ts", ""),
		bash("-", L, "cat src/core/types.ts", ""),
		bash("-", L, "echo x > src/core/other.ts 2>&1", ""),
		bash("-", L, "cp src/core/other.ts /tmp/x", ""),
		bash("-", L, "echo src/core/types.ts", ""),
		bash("lead", P, "echo x > src/core/types.ts", ""),
		bash("-", P, "sed -i '' 's/a/b/' src/core/types.ts", ""),
	})
}

// T-238, T-496: a protected file the lane's stored spec names as an exact path under "Protected files this task may
// edit" is editable in that lane, by Edit and Bash; a glob there, or the path under another heading, unlocks nothing.
func TestProtectedFileUnlockedByExactSpecEntry(t *testing.T) {
	t.Parallel()
	f := fixture(t)
	P, L, S := f.P, f.L, f.S
	unlock := "a lead can unlock it for one task by naming the exact path under the spec's \"Protected files this task may edit\"; don't write it through a script or another tool"
	// Not in the spec at all → denied as today, with the unlock hint for a worker.
	check(t, f.Project, []tc{
		edit("-", L, "Edit", L+"/src/core/types.ts", "src/core/types.ts is a protected file (protected_files in .obor/config.json; lead-only) — don't change it; ask the lead (final message or QUESTIONS.md); "+unlock),
		bash("-", L, "echo x > src/core/types.ts", unlock),
	})
	// Globs that match only → still denied, with the hint; so is the exact path under the old files heading.
	f.Spec("T-01", "# T-01\n\n## Files you may touch\n`src/core/types.ts`\n")
	check(t, f.Project, []tc{edit("-", L, "Edit", L+"/src/core/types.ts", unlock)})
	f.spec("`src/core/*.ts`", "`src/**`")
	check(t, f.Project, []tc{
		edit("-", L, "Edit", L+"/src/core/other.ts", ""),
		edit("-", L, "Edit", L+"/src/core/types.ts", unlock),
		edit("-", L, "Write", L+"/src/core/types.ts", "protected"),
		bash("-", L, "echo x > src/core/types.ts", unlock),
		bash("-", L, "sed -i '' 's/a/b/' src/core/types.ts", "protected"),
	})
	// Listed exactly → the worker in this lane edits it (Edit/Write, redirection, sed -i).
	f.spec("`src/core/types.ts`, `src/a/*`")
	check(t, f.Project, []tc{
		edit("-", L, "Edit", L+"/src/core/types.ts", ""),
		edit("-", L, "Write", L+"/src/core/types.ts", ""),
		edit("-", L+"/src", "Edit", L+"/src/core/../core/types.ts", ""),
		bash("-", L, "echo x > src/core/types.ts", ""),
		bash("-", L, "sed -i '' 's/a/b/' src/core/types.ts", ""),
		bash("-", L, "cd src && echo x >> core/types.ts", ""),
		// Another lane's worker, or a non-worker role → denied as today (no worker hint for them).
		edit("-", S, "Edit", S+"/src/core/types.ts", unlock),
		edit("-", S, "Edit", L+"/src/core/types.ts", "protected"),
		bash("-", S, "echo x > "+L+"/src/core/types.ts", "protected"),
		edit("reviewer", P, "Edit", L+"/src/core/types.ts", "lead-only) — don't change it; ask the lead (final message or QUESTIONS.md)"),
		edit("integrator", P, "Edit", P+"/src/core/types.ts", "protected"),
		bash("integrator", P, "echo x > "+L+"/src/core/types.ts", "protected"),
	})
	code, reason := runAs(f.Project, "reviewer", edit("reviewer", P, "Edit", L+"/src/core/types.ts", "").input(nil))
	if code != 2 || strings.Contains(reason, "can unlock") {
		t.Errorf("reviewer: %d %q, want a deny without the worker hint", code, reason)
	}
	// A later rev without the exact entry → denied again (the current rev is the truth, T-440).
	f.spec("`src/core/*.ts`")
	check(t, f.Project, []tc{
		edit("-", L, "Edit", L+"/src/core/types.ts", unlock),
		bash("-", L, "echo x > src/core/types.ts", "protected"),
	})
}

// RV-25 verify #3, #4 (T-241): an exact spec entry unlocks one regular file (or a path not created yet), never a
// symlink (whatever it points at) or a directory (its children are not listed).
func TestProtectedExactEntryUnlocksOnlyARegularFile(t *testing.T) {
	t.Parallel()
	f := fixture(t)
	L, S := f.L, f.S
	// #3: the lane path is a symlink to another lane's unprotected file → denied (Edit and redirection).
	f.spec("`src/core/types.ts`")
	f.Write(S+"/src/x.ts", "other lane\n")
	if err := os.Symlink(S+"/src/x.ts", L+"/src/core/types.ts"); err != nil {
		t.Fatal(err)
	}
	check(t, f.Project, []tc{
		edit("-", L, "Edit", L+"/src/core/types.ts", "protected"),
		bash("-", L, "echo x > src/core/types.ts", "protected"),
	})
	// Same, the symlink points at an unlisted file of this lane → denied too.
	f.Write(L+"/src/c.txt", "unlisted\n")
	os.Remove(L + "/src/core/types.ts")
	if err := os.Symlink(L+"/src/c.txt", L+"/src/core/types.ts"); err != nil {
		t.Fatal(err)
	}
	check(t, f.Project, []tc{
		edit("-", L, "Edit", L+"/src/core/types.ts", "protected"),
		bash("-", L, "echo x >> src/core/types.ts", "protected"),
	})
	// #4: protected src/core and src/core/**; the spec lists the existing directory src/core → no unlock for its children
	// or for the directory itself.
	os.Remove(L + "/src/core/types.ts")
	f.Config(testkit.Set("protected_files", `["src/core", "src/core/**"]`))
	f.spec("`src/core`")
	check(t, f.Project, []tc{
		bash("-", L, "rm -rf src/core", "protected"),
		bash("-", L, "cp -R incoming/. src/core", "protected"),
		edit("-", L, "Edit", L+"/src/core/types.ts", "protected"),
	})
	// Kept: a regular file listed exactly → Edit and redirection allowed; a path not created yet → Write allowed.
	f.Config(testkit.Set("protected_files", `["src/core/types.ts"]`))
	f.spec("`src/core/types.ts`")
	os.Remove(L + "/src/core/types.ts")
	check(t, f.Project, []tc{edit("-", L, "Write", L+"/src/core/types.ts", "")})
	f.Write(L+"/src/core/types.ts", "regular\n")
	check(t, f.Project, []tc{
		edit("-", L, "Edit", L+"/src/core/types.ts", ""),
		bash("-", L, "echo x > src/core/types.ts", ""),
	})
}

// bash case_nested_shell_double_dash (RV-02 #1)
func TestNestedShellDoubleDashParsesTheScript(t *testing.T) {
	t.Parallel()
	f := fixture(t)
	P, L := f.P, f.L
	cases := []tc{bash("reviewer", P, "bash -c -- 'git push'", "git push is blocked")}
	for _, sh := range []string{"bash", "sh", "zsh", "dash", "ksh", "/bin/sh"} {
		cases = append(cases, bash("reviewer", P, sh+" -c -- 'git push'", "git push"))
	}
	check(t, f.Project, append(cases,
		bash("reviewer", P, "bash -lc -- 'git push'", "git push"),
		bash("reviewer", P, "bash -c -e -- 'git push'", "git push"),
		bash("reviewer", P, "bash -o pipefail -c 'git push'", "git push"),
		bash("reviewer", P, "bash -c -- 'pkill -f x'", "pkill kills by pattern"),
		bash("integrator", P, "sh -c -- 'git commit -am wip'", "git commit -a in the main checkout"),
		bash("-", L, "bash -c -- 'git commit -m x'", "workers don't run git commit"),
		bash("-", L, "zsh -lc -- 'git stash'", "workers don't run git stash"),
		bash("lead", L, "zsh -lc -- 'git stash'", "workers don't run git stash"),
		bash("reviewer", P, "bash -c -- 'echo x > src/core/types.ts'", "src/core/types.ts is a protected file"),
		bash("reviewer", P, "dash -c -- 'cp /tmp/x src/core/types.ts'", "protected"),
		bash("reviewer", P, "bash -c -- 'echo git push'", ""),
		bash("reviewer", P, "bash -c -- 'git status'", ""),
		bash("reviewer", P, "bash -- script.sh -c 'git push'", ""),
		bash("reviewer", P, "bash script.sh -c -- 'git push'", ""),
		bash("-", L, "sh -c -- 'git diff'", ""),
	))
}

// bash case_cd_options (RV-02 #2)
func TestCdOptionsMoveTheCwd(t *testing.T) {
	t.Parallel()
	f := fixture(t)
	P, R := f.P, f.Base+"/review"
	f.Git(P, "worktree", "add", "-q", "--detach", R, "main")
	os.MkdirAll(R+"/-sub", 0o755)
	check(t, f.Project, []tc{
		bash("integrator", R, "cd -- '"+P+"' && git commit -am x", "git commit -a in the main checkout"),
		bash("integrator", R, "cd -P "+P+" && git commit -am x", "commit -a"),
		bash("integrator", R, "cd -L "+P+" && git commit -am x", "commit -a"),
		bash("integrator", R, "cd -LP -- "+P+"; git commit -am x", "commit -a"),
		bash("integrator", P, "cd -- '"+R+"' && git commit -am x", ""),
		bash("integrator", P, "cd -P "+R+" && git commit -am x", ""),
		bash("integrator", P, "cd -- "+R+"/-sub && git commit -am x", ""),
		bash("integrator", R, "cd -- '"+R+"' && git commit -am x", ""),
	})
}

// bash case_cd_subshell_scope (RV-02 #2)
func TestCdScopedBySubshellPipelineAndNestedShell(t *testing.T) {
	t.Parallel()
	f := fixture(t)
	P, R := f.P, f.Base+"/review"
	f.Git(P, "worktree", "add", "-q", "--detach", R, "main")
	check(t, f.Project, []tc{
		bash("integrator", P, "(cd '"+R+"'); git commit -am x", "git commit -a in the main checkout"),
		bash("integrator", P, "(cd "+R+") && git commit -am x", "commit -a"),
		bash("integrator", P, "cd "+R+" | cat; git commit -am x", "commit -a"),
		bash("integrator", P, "cd "+R+" | git commit -am x", "commit -a"),
		bash("integrator", P, "cd "+R+" & git commit -am x", "commit -a"),
		bash("integrator", P, "echo $(cd "+R+"); git commit -am x", "commit -a"),
		bash("integrator", P, "bash -c 'cd "+R+"'; git commit -am x", "commit -a"),
		bash("integrator", R, "(cd "+P+" && (cd "+R+") && git commit -am x)", "commit -a"),
		bash("integrator", P, "(cd "+R+" && git commit -am x)", ""),
		bash("integrator", P, "(cd "+R+"; git commit -am x); echo ok", ""),
		bash("integrator", R, "(cd '"+P+"'); git commit -am x", ""),
		bash("integrator", R, "cd "+P+" | cat; git commit -am x", ""),
		bash("integrator", R, "cd "+P+" & git commit -am x", ""),
		bash("integrator", P, "cd "+R+" && git status | cat && git commit -am x", ""),
		bash("integrator", P, "case x in x) cd "+R+";; esac; git commit -am x", ""),
		bash("integrator", P, "if true; then cd "+R+"; fi; git commit -am x", ""),
	})
}

// bash case_protected_after_cd_options_and_scope (RV-02 #2)
func TestProtectedAfterCdUsesTheRightCwd(t *testing.T) {
	t.Parallel()
	f := fixture(t)
	L := f.L
	check(t, f.Project, []tc{
		bash("-", L, "cd -- src && echo x > core/types.ts", "src/core/types.ts is a protected file"),
		bash("-", L, "cd -P src && cp /tmp/x core/types.ts", "protected"),
		bash("-", L, "(cd src); echo x > src/core/types.ts", "protected"),
		bash("-", L, "cd src | true; tee src/core/types.ts < /dev/null", "protected"),
		bash("-", L, "(cd src); echo x > core/types.ts", ""),
		bash("-", L, "cd src | true; echo x > core/types.ts", ""),
		bash("-", L, "cd -- src && echo x > src/core/types.ts", ""),
	})
}

// bash case_cp_source_not_a_target (RV-02 #8)
func TestCpChecksItsTargetNotItsSources(t *testing.T) {
	t.Parallel()
	f := fixture(t)
	P, L, out := f.P, f.L, f.Base+"/outside"
	f.Write(out+"/types.ts", "")
	check(t, f.Project, []tc{
		bash("integrator", P, "cp src/core/types.ts /tmp/copy", ""),
		bash("integrator", P, "cp src/core/types.ts "+out+"/copy.ts", ""),
		bash("integrator", P, "cp -p src/core/types.ts src/core/types.ts.bak", ""),
		bash("integrator", P, "cp -- src/core/types.ts src/core/other.ts "+out, ""),
		bash("integrator", P, "cp -t "+out+" src/core/types.ts", ""),
		bash("-", L, "cp src/core/types.ts /tmp/copy", ""),
		bash("integrator", P, "cp /tmp/other.ts src/core/", ""),
		bash("integrator", P, "cp /tmp/x src/core/types.ts", "src/core/types.ts is a protected file"),
		bash("integrator", P, "cp -f -- /tmp/x src/core/types.ts", "protected"),
		bash("integrator", P, "cp "+out+"/types.ts src/core/", "protected"),
		bash("integrator", P, "cp -r "+out+"/types.ts src/core", "protected"),
		bash("integrator", P, "cp -t src/core "+out+"/types.ts", "protected"),
		bash("integrator", P, "cp --target-directory=src/core "+out+"/types.ts", "protected"),
		bash("integrator", P, "mv src/core/types.ts /tmp/x", "protected"),
	})
}

// bash case_no_config_allows_everything
func TestNoConfigAllowsEverything(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	lane := p.Lane("L-01", "task/T-01")
	if p.Exists(".obor/config.json") {
		t.Fatal("precondition: no config")
	}
	os.MkdirAll(p.Base+"/nogit", 0o755)
	defer check(t, p, []tc{bash("-", p.Root, "git push", "git push is blocked")}) // precondition: with a config, denied
	defer p.Config()
	check(t, p, []tc{
		bash("-", p.Root, "git push", ""),
		bash("-", p.Root, "pkill -f x", ""),
		bash("integrator", p.Root, "git commit -am x", ""),
		bash("-", lane, "git commit -m x", ""),
		bash("worker", lane, "echo x > src/core/types.ts", ""),
		edit("worker", lane, "Edit", p.Root+"/README.md", ""),
		bash("-", p.Base+"/nogit", "git push", ""),
	})
}

// bash case_role_from_env_and_stdin_cwd_only
func TestRoleFromEnvAndEventCwdNotClaudeProjectDir(t *testing.T) {
	t.Parallel()
	f := fixture(t)
	P, L := f.P, f.L
	// cwd = lane, no OBOR_ROLE → worker, even when CLAUDE_PROJECT_DIR says main.
	check(t, f.Project, []tc{bash("-", L, "git commit -m x", "workers don't run git commit")}, "CLAUDE_PROJECT_DIR="+P)
	// cwd = main, CLAUDE_PROJECT_DIR = lane, no OBOR_ROLE → none: allowed.
	check(t, f.Project, []tc{bash("-", P, "git commit -m x", "")}, "CLAUDE_PROJECT_DIR="+L)
	check(t, f.Project, []tc{
		bash("worker", P, "git stash", "workers don't run git stash"),
		bash("lead", L, "git commit -m x", "workers don't run git commit"), // env lead in a lane → worker
	})
}

// bash case_other_tools_and_bad_input
func TestOtherToolsAndBadInputAllow(t *testing.T) {
	t.Parallel()
	f := fixture(t)
	noCfg := f.Base + "/plain"
	f.Git(f.Base, "init", "-q", noCfg)
	check(t, f.Project, []tc{bash("-", f.L, "git push", "git push is blocked")}) // precondition: the guard runs here
	check(t, f.Project, []tc{
		edit("-", f.L, "Read", f.L+"/src/core/types.ts", ""),
		bash("-", f.Base+"/does-not-exist", "git push", ""),
		bash("-", noCfg, "git push", ""),
		bash("-", f.Base, "git push", ""), // not a repo
	})
	for _, in := range []string{"", "{ not json", "[]", `{"tool_name":"Bash","cwd":"` + f.P + `","tool_input":{}}`} {
		if code, reason := runAs(f.Project, "-", []byte(in)); code != 0 || reason != "" {
			t.Errorf("input %q: %d %q", in, code, reason)
		}
	}
}

// bash case_timing_50_runs (OBOR_TEST_PERF): go test -run - -bench Guard ./internal/guard
func BenchmarkGuardBash(b *testing.B) {
	b.StopTimer()
	f := fixture(b)
	in := bash("-", f.L, "cd src && git status --short && echo ok > /tmp/obor-x.txt", "").input(nil)
	b.StartTimer()
	for i := 0; i < b.N; i++ {
		if code, reason := Run(in, f.Env()); code != 0 {
			b.Fatal(reason)
		}
	}
}

// bash case_one_lead_unchanged: the lead edits anything, protected files included.
func TestLeadEditsAnything(t *testing.T) {
	t.Parallel()
	f := fixture(t)
	P := f.P
	check(t, f.Project, []tc{
		edit("lead pj-lead", P, "Edit", P+"/src/core/types.ts", ""),
		bash("lead pj-lead", P, "echo x > src/core/types.ts", ""),
		edit("lead pj-lead", P, "Edit", P+"/src/net/socket.ts", ""),
		edit("integrator", P, "Edit", P+"/src/core/types.ts", "lead-only) — don't change it; ask the lead"),
	})
}

// bash case_no_identity_from_session_id (T-159)
func TestNoIdentityFromTheSessionID(t *testing.T) {
	t.Parallel()
	f := fixture(t)
	P, home := f.P, f.Base+"/state-home"
	f.Write(home+"/sessions/sid-o1", "pj-l-01\tworker\tL-01\n")
	f.Write(home+"/sessions/sid-int", "pj-int\tintegrator\t\n")
	call := func(who string, c tc, sid string, env ...string) (int, string) {
		return runAs(f.Project, who, c.input(map[string]any{"session_id": sid}), env...)
	}
	if code, reason := call("worker pj-l-01", bash("", P, "git commit -m x", ""), "sid-o1", "OBOR_LANE=L-01"); code != 2 || !strings.Contains(reason, "git") {
		t.Errorf("precondition: worker env → git guard: %d %q", code, reason)
	}
	if code, reason := call("integrator pj-int", edit("", P, "Edit", P+"/src/core/types.ts", ""), "sid-int"); code != 2 || !strings.Contains(reason, "protected") {
		t.Errorf("precondition: integrator env → protected denied: %d %q", code, reason)
	}
	if code, reason := call("-", bash("", P, "git commit -m x", ""), "sid-o1"); code != 0 {
		t.Errorf("worker's session id → no worker guard: %q", reason)
	}
	if code, reason := call("-", edit("", P, "Edit", P+"/src/core/types.ts", ""), "sid-int"); code != 0 {
		t.Errorf("integrator's session id → no integrator rules: %q", reason)
	}
}

// bash case_reviews_write_profile (T-193)
func TestReviewsWriteProfile(t *testing.T) {
	t.Parallel()
	f := fixture(t)
	P, S := f.P, f.S
	f.Config(testkit.Set("protected_files", `["src/core/types.ts"]`), testkit.Set("paths.research", "docs/research"))
	var cases []tc
	for _, rv := range []string{"reviewer", "deep_dive"} {
		cases = append(cases,
			edit(rv, P, "Write", P+"/docs/reviews/R-01.md", ""),
			edit(rv, P, "Edit", P+"/docs/reviews/sub/R-01-verify.md", ""),
			edit(rv, P, "Write", P+"/docs/research/probe.md", ""),
			edit(rv, P, "Write", P+"/.task-done.md", ".task-done.md is outside the "+rv+" write profile"),
			edit(rv, P, "Write", f.Base+"/outside/scratch.txt", ""),
			edit(rv, P, "Edit", P+"/src/a/x.ts", "src/a/x.ts is outside the "+rv+" write profile — a "+rv+" writes only under docs/reviews, docs/research in the main checkout "+P+"; report the change as a finding instead"),
			edit(rv, P, "Write", P+"/docs/reviews-old/x.md", "outside the "+rv+" write profile"),
			edit(rv, P, "Write", S+"/.task-done.md", "outside the "+rv+" write profile"),
			edit(rv, P, "Edit", P+"/src/core/types.ts", "src/core/types.ts is a protected file"))
	}
	// T-853: from its role worktree a reviewer or deep dive writes its files into the main checkout's reviews dir, never
	// into its worktree's own.
	for _, rv := range []string{"reviewer", "deep_dive"} {
		wt := f.LaneDir(rv)
		f.Git(P, "worktree", "add", "-q", "--detach", wt, "main")
		cases = append(cases,
			edit(rv, wt, "Write", P+"/docs/reviews/R-01.md", ""),
			bash(rv, wt, "echo x > "+P+"/docs/reviews/R-01.md", ""),
			edit(rv, wt, "Write", wt+"/docs/reviews/R-01.md", "writes only under docs/reviews, docs/research in the main checkout "+P),
			bash(rv, wt, "echo x > docs/reviews/R-01.md", "in the main checkout "+P),
			edit(rv, wt, "Edit", wt+"/src/a/x.ts", "outside the "+rv+" write profile"))
	}
	check(t, f.Project, append(cases,
		bash("reviewer", P, "echo x > src/a/x.ts", "src/a/x.ts is outside the reviewer write profile"), // RV-83 #1
		bash("integrator", P, "echo x > src/a/x.ts", ""),
		edit("integrator", P, "Edit", P+"/src/a/x.ts", ""),
		edit("advisor", P, "Edit", P+"/src/a/x.ts", "src/a/x.ts is outside the advisor write profile"), // T-801
	))
	f.Config(testkit.Set("paths.reviews", "notes/rv"))
	check(t, f.Project, []tc{
		edit("reviewer", P, "Write", P+"/notes/rv/R-02.md", ""),
		edit("reviewer", P, "Write", P+"/docs/reviews/R-02.md", "writes only under notes/rv"),
	})
}

// T-704, T-801: the researcher (a kit default custom row) writes only under docs/research/, the advisor only under
// docs/research/ and docs/plan/; both run no git write and read anywhere. An object row replaces the write list.
func TestResearcherWriteProfile(t *testing.T) {
	t.Parallel()
	f := fixture(t)
	P := f.P
	f.Config(testkit.Set("roles.researcher", "sonnet"), testkit.Set("roles.advisor", "opus")) // a string: the default rows
	check(t, f.Project, []tc{
		edit("researcher", P, "Write", P+"/docs/research/x.md", ""),
		edit("researcher", P, "Edit", P+"/docs/research/sub/y.md", ""),
		edit("researcher", P, "Write", f.Base+"/outside/scratch.txt", ""),
		edit("researcher", P, "Edit", P+"/internal/x.go", "internal/x.go is outside the researcher write profile — a researcher writes only docs/research/"),
		edit("researcher", P, "Write", P+"/docs/reviews/R-01.md", "outside the researcher write profile"),
		edit("researcher", P, "Write", P+"/docs/research-old/x.md", "outside the researcher write profile"),
		edit("researcher", P, "Write", P+"/docs/plan/p.md", "outside the researcher write profile"),
		bash("researcher", P, "echo x > internal/x.go", "outside the researcher write profile"),
		bash("researcher", P, "git commit -m x", "a researcher doesn't run git commit"),
		bash("researcher", P, "git add docs/research/x.md", "a researcher doesn't run git add"),
		bash("researcher", P, "git push", "git push"),
		bash("researcher", P, "git status", ""),
		bash("researcher", P, "git log -3", ""),
		bash("researcher", P, "cat internal/x.go", ""),
		bash("advisor", P, "git status", ""),
		bash("advisor", P, "git commit -m x", "an advisor doesn't run git commit"),
		edit("advisor", P, "Write", P+"/docs/research/topic-advice.md", ""),
		edit("advisor", P, "Write", P+"/docs/plan/milestone.md", ""),
		edit("advisor", P, "Edit", P+"/internal/x.go", "internal/x.go is outside the advisor write profile — an advisor writes only docs/research/, docs/plan/"),
	})
	f.Config(testkit.Set("roles.researcher", `{"model": "sonnet", "write": ["notes/res/"]}`))
	check(t, f.Project, []tc{
		edit("researcher", P, "Write", P+"/notes/res/x.md", ""),
		edit("researcher", P, "Write", P+"/docs/research/x.md", "writes only notes/res/"),
	})
}

// T-801: a custom role writes exactly its write list (a dir/ entry: under it; a file entry: that file), elsewhere in
// the project is denied (outside it is not limited); it runs git reads only and never pushes, even when allow_push
// lists it; a string row writes nothing.
func TestCustomRoleGuard(t *testing.T) {
	t.Parallel()
	f := fixture(t)
	P := f.P
	f.Write("docs/agents/designer.md", "# Role: designer\n")
	f.Write("docs/agents/buddy.md", "# Role: buddy\n")
	f.Config(testkit.Set("protected_files", `["src/core/types.ts"]`),
		testkit.Set("roles.designer", `{"model": "opus", "write": ["docs/design/", "STYLE.md"]}`),
		testkit.Set("roles.buddy", "sonnet"), testkit.Set("guards.allow_push", `["designer"]`))
	check(t, f.Project, []tc{
		edit("designer", P, "Write", P+"/docs/design/a.md", ""),
		edit("designer", P, "Edit", P+"/docs/design/sub/b.md", ""),
		edit("designer", P, "Write", P+"/STYLE.md", ""),
		edit("designer", P, "Write", f.Base+"/outside/scratch.txt", ""),
		edit("designer", P, "Write", P+"/STYLE.md.bak", "STYLE.md.bak is outside the designer write profile — a designer writes only docs/design/, STYLE.md"),
		edit("designer", P, "Write", P+"/docs/design-old/x.md", "outside the designer write profile"),
		edit("designer", P, "Edit", P+"/internal/x.go", "outside the designer write profile"),
		edit("designer", P, "Edit", P+"/src/core/types.ts", "src/core/types.ts is a protected file"),
		bash("designer", P, "echo x > internal/x.go", "outside the designer write profile"),
		bash("designer", P, "echo x > docs/design/c.md", ""),
		bash("designer", P, "git commit -m x", "a designer doesn't run git commit"),
		bash("designer", P, "git add docs/design/a.md", "a designer doesn't run git add"),
		bash("designer", P, "git push origin main", "git push"),
		bash("designer", P, "git status", ""),
		bash("designer", P, "git log -3", ""),
		edit("buddy", P, "Write", P+"/docs/x.md", "docs/x.md is outside the buddy write profile — a buddy writes no file in the project"),
		bash("buddy", P, "git status", ""),
	})
}

// T-814 (RV-104 #1): a custom role whose roles.<name> row the config no longer names — removed while its agent runs —
// fails closed: editor and shell writes in the project and every git write are denied with a reason naming the missing
// row, git reads stay allowed; the kit's default rows are unaffected and a lane cwd still resolves to the worker.
func TestRemovedCustomRoleFailsClosed(t *testing.T) {
	t.Parallel()
	const why = "roles.ux_buddy is not in .obor/config.json"
	f := fixture(t)
	P, L := f.P, f.L
	f.Write("docs/agents/ux_buddy.md", "# Role: ux_buddy\n")
	f.Config(testkit.Set("protected_files", `["src/core/types.ts"]`),
		testkit.Set("roles.ux_buddy", `{"model": "opus", "write": ["docs/ux/"]}`))
	// Precondition: with its row the role writes its list and reads git, so the denials below come from the removal.
	check(t, f.Project, []tc{
		edit("ux_buddy", P, "Write", P+"/docs/ux/a.md", ""),
		bash("ux_buddy", P, "git add docs/ux/a.md", "an ux_buddy doesn't run git add"),
		bash("ux_buddy", P, "git status", ""),
	})
	f.Config(testkit.Set("protected_files", `["src/core/types.ts"]`)) // the row removed while the agent runs
	check(t, f.Project, []tc{
		edit("ux_buddy", P, "Edit", P+"/internal/x.go", why),
		edit("ux_buddy", P, "Write", P+"/docs/ux/a.md", why),
		bash("ux_buddy", P, "echo x > internal/x.go", why),
		bash("ux_buddy", P, "cp README.md internal/x.go", why),
		bash("ux_buddy", P, "git add docs/ux/a.md", why),
		bash("ux_buddy", P, "git commit -m x", why),
		bash("ux_buddy", P, "git status", ""),
		bash("ux_buddy", P, "git log -3", ""),
		// The kit's default rows (advisor, researcher, triage) are unaffected.
		edit("advisor", P, "Write", P+"/docs/plan/m.md", ""),
		edit("researcher", P, "Write", P+"/docs/research/r.md", ""),
		edit("triage", P, "Write", P+"/docs/BUGS.md", ""),
		bash("advisor", P, "git commit -m x", "an advisor doesn't run git commit"),
		// A lane cwd still resolves the session to the worker, whatever its env role.
		edit("ux_buddy", L, "Edit", L+"/src/a/x.go", ""),
	})
}

// bash case_old_bypasses_denied (T-193)
func TestOldBashBypassesDenied(t *testing.T) {
	t.Parallel()
	f := fixture(t)
	P, L, out := f.P, f.L, f.Base+"/outside"
	f.Write(out+"/types.ts", "")
	f.Write(P+"/src/core/types.ts", "")
	if err := os.Symlink(P+"/src/core/types.ts", L+"/link.ts"); err != nil {
		t.Fatal(err)
	}
	check(t, f.Project, []tc{
		// eval, variables, $( ) in double quotes, functions, nested shells, xargs, find -exec.
		bash("-", P, "eval git push", "git push is blocked"),
		bash("-", P, "eval 'git push origin main'", "git push"),
		bash("-", P, "CMD='git push'; $CMD", "git push"),
		bash("-", P, "export SUB=push && git $SUB", "git push"),
		bash("-", P, "declare -x S=push; git $S", "git push"),
		bash("-", P, "git $OBOR_TEST_GIT_SUB", "git push"), // from the environment
		bash("-", P, `echo "$(git push)"`, "git push"),
		bash("-", P, "x=`git push`", "git push"),
		bash("-", P, "diff <(git push) /dev/null", "git push"),
		bash("-", P, "f() { git push; }; f", "git push"),
		bash("-", P, `bash -c "sh -c 'git push'"`, "git push"),
		bash("-", P, `bash -c "sh -c 'zsh -c \"git push\"'"`, "git push"),
		bash("-", P, "echo origin | xargs git push", "git push"),
		bash("-", P, "echo origin | xargs -n1 git push", "git push is blocked"),
		bash("-", P, `find . -name x -exec git push \;`, "git push"),
		bash("-", P, `find . -execdir git push \;`, "git push"),
		bash("-", P, "cat <<EOF\n$(pkill -f x)\nEOF", "pkill"),
		bash("-", L, "eval git stash", "workers don't run git stash"),
		// GIT_DIR / --git-dir / --work-tree, cd to a variable.
		bash("integrator", L, "git --git-dir="+P+"/.git --work-tree="+P+" commit -am x", "git commit -a in the main checkout"),
		bash("integrator", L, "git --git-dir="+P+"/.git commit -am x", "commit -a"),
		bash("integrator", L, "git --work-tree "+P+" commit -a", "commit -a"),
		bash("integrator", L, "git --work-tree="+P+" commit -am x", "commit -a"),
		bash("integrator", L, "GIT_DIR="+P+"/.git git commit -am x", "commit -a"),
		bash("integrator", L, "D="+P+"; cd $D && git commit -am x", "commit -a"),
		bash("integrator", L, "D="+P+`; cd "$D" && git commit -am x`, "commit -a"),
		// Writers: mv into a directory, git checkout/restore, symlinks.
		bash("integrator", P, "mv "+out+"/types.ts src/core", "src/core/types.ts is a protected file"),
		bash("integrator", P, "git checkout main -- src/core/types.ts", "protected"),
		bash("integrator", P, "git restore --source main src/core/types.ts", "protected"),
		bash("-", L, "echo x > link.ts", "src/core/types.ts is a protected file"),
		edit("-", L, "Write", L+"/link.ts", "src/core/types.ts is a protected file"),
		// Look-alikes stay allowed.
		bash("-", P, "echo 'eval git push'", ""),
		bash("-", P, `X='git push'; echo "$X"`, ""),
		bash("-", P, `find . -name '*.go' -exec grep push {} \;`, ""),
		bash("-", P, "echo a | xargs echo git push", ""),
		bash("integrator", P, "git checkout main", ""), // T-853: the main checkout's HEAD rows went
		bash("integrator", P, "git --git-dir="+L+"/.git commit -am x", ""),
		bash("-", L, "bash tests/a.test.sh", ""),
	}, "OBOR_TEST_GIT_SUB=push")
}

// Wrappers (T-220, T-230): the wrapper word and its valueOpts' values are skipped, any other option is a flag; a
// git/pkill/killall word among the skipped words, or after a flag, is checked as a command too (fail closed).
func TestWrapperOptions(t *testing.T) {
	t.Parallel()
	f := fixture(t)
	P, L := f.P, f.L
	check(t, f.Project, []tc{
		// The wrapper word: the nested command is checked.
		bash("-", P, "env FOO=1 git push", "git push is blocked"),
		bash("-", P, "nohup git push", "git push is blocked"),
		bash("-", P, "timeout 5 git push", "git push is blocked"),
		bash("-", P, "command time git push", "git push is blocked"),
		bash("-", P, "xargs -- git push", "git push is blocked"),
		bash("-", P, "timeout 5 pkill x", "pkill kills by pattern"),
		bash("-", P, "timeout 5 echo git push", ""),
		// valueOpts take the next word: what follows it is the command, the value is not run.
		bash("-", P, "sudo -u root git push", "git push is blocked"),
		bash("-", P, "nice -n 5 git push", "git push is blocked"),
		bash("-", P, "xargs -I {} git push {}", "git push is blocked"),
		bash("-", P, "timeout -s KILL -k 5 10 git push", "git push is blocked"),
		bash("-", P, "env -u X git push", "git push is blocked"),
		bash("-", P, "sudo -u me env -C sub nice -n 5 xargs git push", "git push is blocked"),
		bash("-", P, "xargs -I git echo push", ""),
		bash("-", P, "env -u git echo push", ""),
		bash("-", P, "nice -n 5 echo git push", ""),
		// Flags take none.
		bash("-", P, "env -i git push", "git push is blocked"),
		bash("-", P, "sudo -E git push", "git push is blocked"),
		bash("-", P, "command -v git", ""),
		bash("lead", P, "xargs -0 git log", ""),
		// env -C / sudo -D name the repo of the nested git.
		bash("integrator", L, "env -C "+P+" git commit -am x", "git commit -a in the main checkout"),
		bash("integrator", L, "sudo -D "+P+" git commit -a", "commit -a"),
		bash("integrator", P, "env -C "+L+" git commit -am x", ""),
		// Any other option is a flag; it may still take the next word, so a later git/pkill/killall word (or a blank-
		// separated part of one: env -S 'git push') is checked as a command too.
		bash("-", P, "env --bogus git push", "git push is blocked"),
		bash("-", P, "sudo -X git push", "git push is blocked"),
		bash("-", P, "env -S 'git push'", "git push is blocked"),
		bash("-", P, "nice -z 'pkill x'", "pkill kills by pattern"),
		bash("-", P, "xargs --max-args=1 /usr/bin/killall x", "killall kills by pattern"),
		bash("-", P, "sudo --user root git push", "git push is blocked"),
		bash("-", P, "sudo -nu root git push", "git push is blocked"),
		bash("-", P, "xargs -0 echo git push", "git push is blocked"),
		bash("-", P, "env -X git status", ""),
		bash("-", P, "env -X echo hi", ""),
		bash("-", P, "sudo --frobnicate=1 ls", ""),
		// A value never hides git/pkill/killall: it is checked as a command (env -u git push may run push or git).
		bash("-", P, "env -u git push", "git push is blocked"),
		bash("-", P, "timeout pkill x", "pkill kills by pattern"),
	})
}

// RV-24 #4 (T-230): value options are per wrapper — sudo -n/-k/-s are flags, so the command after them is checked.
func TestWrapperValueOptionsPerWrapper(t *testing.T) {
	t.Parallel()
	f := fixture(t)
	P := f.P
	check(t, f.Project, []tc{
		bash("-", P, "sudo -n git push", "git push is blocked"),
		bash("-", P, "sudo -k git push", "git push is blocked"),
		bash("-", P, "sudo -s git push", "git push is blocked"),
		bash("-", P, "sudo -k pkill claude", "pkill kills by pattern"),
		bash("-", P, "sudo -s killall node", "killall kills by pattern"),
		bash("-", P, "sudo -u root git push", "git push is blocked"),
		bash("-", P, "sudo -p PROMPT git push", "git push is blocked"),
		bash("-", P, "env -u X git push", "git push is blocked"),
		bash("-", P, "xargs -n1 git push", "git push is blocked"),
		bash("-", P, "xargs -n 1 git push", "git push is blocked"),
		bash("-", P, "timeout 5 git push", "git push is blocked"),
		bash("-", P, "nice -n 5 pkill x", "pkill kills by pattern"),
		bash("-", P, "stdbuf -o L git push", "git push is blocked"),
		bash("-", P, "exec -a name git push", "git push is blocked"),
		bash("-", P, "sudo -k git status", ""),
		bash("-", P, "sudo -n ls", ""),
		bash("-", P, "env -u X ls", ""),
	})
}

// find (T-220): the words after -exec/-execdir/-ok/-okdir up to ; or + are a nested command; other words are words.
func TestFindExec(t *testing.T) {
	t.Parallel()
	f := fixture(t)
	P := f.P
	check(t, f.Project, []tc{
		bash("-", P, `find . -name x -exec git push \;`, "git push is blocked"),
		bash("-", P, `find . -okdir git push {} +`, "git push is blocked"),
		bash("-", P, `find . -name x -exec echo {} \; -exec pkill x \;`, "pkill kills by pattern"),
		bash("-", P, "find . -name git -exec echo push {} +", ""),
		bash("-", P, "find . -name push -path git", ""),
	})
}

// bash case_unresolved_command (T-199, RV-22-verify #2)
func TestUnresolvedCommandWordWithGitDenied(t *testing.T) {
	t.Parallel()
	f := fixture(t)
	P := f.P
	check(t, f.Project, []tc{
		bash("-", P, "git${RV22_UNSET} push", "can't be resolved"),
		bash("-", P, `"g""it" push`, "git push is blocked"),
		bash("-", P, "$(echo git) push", "$(echo git) can't be resolved"),
		bash("-", P, "`printf git` push", "can't be resolved"),
		bash("-", P, "${GIT:-git} push", "${GIT:-git} can't be resolved"),
		bash("-", P, `sh -c "$CMD" _ git push`, "$CMD can't be resolved"),
		bash("-", P, `sh -c '$1 push' _ git`, "$1 can't be resolved"),
		bash("-", P, `eval "$CMD" git push`, "can't be resolved"),
		bash("-", P, `find . -name git -exec "$X" push \;`, "can't be resolved"),
		bash("-", P, "sudo -u root $X git push", "can't be resolved"),
		bash("-", P, "/usr/bin/gi? push", "/usr/bin/gi? can't be resolved"),
		bash("-", P, "$((1)) pkill x", "pkill is in the command"),
		bash("-", P, "{killall,x} x", "killall is in the command"),
		// Controls: resolved commands with unresolved arguments, unresolved commands without git.
		bash("-", P, `echo "$HOME"`, ""),
		bash("-", P, "ls ${dir}/x", ""),
		bash("-", P, "go test ./...", ""),
		bash("-", P, `make -C "$d" build`, ""),
		bash("-", P, `cd "$WT" && ls`, ""),
		bash("-", P, `"$EDITOR" notes.txt`, ""),
		bash("-", P, `sh -c "$CMD"`, ""),
		bash("-", P, "[ -d .git ] && echo yes", ""),
		bash("-", P, "echo '$X' && git status", ""),
	})
}

// bash case_unresolved_command_verb (T-200)
func TestUnresolvedCommandWordDeniesOnlyWithAWriteVerb(t *testing.T) {
	t.Parallel()
	f := fixture(t)
	P := f.P
	check(t, f.Project, []tc{
		bash("-", P, "$(echo git) push", "push is in the command"),
		bash("-", P, "${G:-git} push origin", "${G:-git} can't be resolved"),
		bash("-", P, "`printf git` commit -m x", "commit is in the command"),
		bash("-", P, "$X git reset --hard", "reset is in the command"),
		bash("-", P, "$X /usr/bin/killall x", "/usr/bin/killall is in the command"),
		bash("-", P, "$((1)) pkill x", "pkill is in the command"),
		bash("-", P, "pk* x", "pkill is in the command"),
		bash("-", P, `sh -c '"$G" stash'`, "stash is in the command"),
		// No dangerous verb after the unresolved word: allowed.
		bash("-", P, `"$(git rev-parse --show-toplevel)"/scripts/x.sh`, ""),
		bash("-", P, "$EDITOR .gitignore", ""),
		bash("-", P, `"$(go env GOPATH)"/bin/x`, ""),
		bash("-", P, "$CC -o out main.c", ""),
		bash("-", P, "$X git status", ""),
		bash("-", P, `$X "$(echo push)"`, ""),
		bash("-", P, "$X $(echo git) log", ""),
		bash("-", P, `sh -c '"$TOOL" --gitdir x'`, ""),
		// A substitution's own words are still walked as their own command.
		bash("-", P, `"$(git push)"/x.sh`, "git push is blocked"),
	})
}

func TestUnescapeAndFields(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		in     string
		quoted bool
		want   string
	}{{`a\ b`, false, "a b"}, {`\$x`, false, "$x"}, {"a\\\nb", false, "ab"}, {`\n\"`, true, `\n"`}, {`\$\\`, true, `$\`}} {
		if got := unescape(c.in, c.quoted); got != c.want {
			t.Errorf("unescape(%q, %v) = %q, want %q", c.in, c.quoted, got, c.want)
		}
	}
}

// RV-83 #1-3 (T-706): one path policy per role for every write route. A researcher (docs/research) and a reviewer /
// deep_dive (docs/reviews) are held to their write profile by shell write targets as by the editor tools, a final
// symlink is checked at its destination, and they run git reads only.
func TestRV83WriteProfileOnEveryRoute(t *testing.T) {
	t.Parallel()
	f := fixture(t)
	P := f.P
	f.Config(testkit.Set("protected_files", `["src/core/types.ts"]`), testkit.Set("paths.research", "docs/research"))
	f.Write(P+"/internal/a.go", "package a\n")
	for _, d := range []string{"docs/research", "docs/reviews"} {
		os.MkdirAll(P+"/"+d, 0o755)
		f.Write(P+"/"+d+"/a.md", "x\n")
		link(t, P+"/"+d+"/l", P+"/internal/a.go")
		link(t, P+"/"+d+"/loop", P+"/"+d+"/loop")
	}
	if fi, err := os.Lstat(P + "/docs/research/l"); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("precondition: docs/research/l is a symlink")
	}
	var cases []tc
	for _, c := range []struct{ role, dir string }{{"researcher", "docs/research"}, {"reviewer", "docs/reviews"},
		{"deep_dive", "docs/reviews"}} {
		rl, d := c.role, c.dir
		out := "outside the " + rl + " write profile"
		cases = append(cases,
			// Editor tools.
			edit(rl, P, "Write", P+"/"+d+"/a.md", ""),
			edit(rl, P, "Write", P+"/internal/a.go", "internal/a.go is "+out),
			edit(rl, P, "Write", P+"/"+d+"/l", d+"/l is a symlink to "+P+"/internal/a.go: internal/a.go is "+out),
			edit(rl, P, "Edit", P+"/"+d+"/loop", "is a symlink that cannot be resolved"),
			// Shell write targets.
			bash(rl, P, "printf x > internal/a.go", "internal/a.go is "+out),
			bash(rl, P, "printf x >> "+d+"/a.md", ""),
			bash(rl, P, "echo x | tee internal/a.go", "internal/a.go is "+out),
			bash(rl, P, "cp "+d+"/a.md internal/", "internal/a.md is "+out),
			bash(rl, P, "sed -i '' s/a/b/ internal/a.go", "internal/a.go is "+out),
			bash(rl, P, "rm internal/a.go", "internal/a.go is "+out),
			bash(rl, P, "sed -i s/a/b/ internal/a.go", "internal/a.go is "+out),
			bash(rl, P, "sed -i '' s/a/b/ "+d+"/a.md", ""),
			bash(rl, P, "mv internal/a.go "+d+"/", "internal/a.go is "+out),
			bash(rl, P, "mv "+d+"/a.md "+d+"/", ""),
			bash(rl, P, "mv "+d+"/a.md internal/b.go", "internal/b.go is "+out),
			bash(rl, P, "printf x > "+d+"/l", d+"/l is a symlink to "+P+"/internal/a.go"),
			bash(rl, P, "echo x | tee "+d+"/loop", "cannot be resolved"),
			bash(rl, P, "printf x > src/core/types.ts", "src/core/types.ts is a protected file"),
			// Reads and writes outside the repo.
			bash(rl, P, "cat internal/a.go", ""),
			bash(rl, P, "cp internal/a.go "+d+"/", ""),
			bash(rl, P, "go vet ./... > /dev/null 2>&1", ""),
			bash(rl, P, "printf x > "+f.Base+"/outside/scratch.txt", ""),
			// git: reads only.
			bash(rl, P, "git log", ""),
			bash(rl, P, "git branch", ""),
			bash(rl, P, "git branch -av", ""),
			bash(rl, P, "git branch --list 'task/*'", ""),
			bash(rl, P, "git config --get user.name", ""),
			bash(rl, P, "git status --short", ""),
			bash(rl, P, "git branch x", "doesn't run git branch x"),
			bash(rl, P, "git branch -D x", "doesn't run git branch -D"),
			bash(rl, P, "git branch -m x y", "doesn't run git branch"),
			bash(rl, P, "git update-ref refs/heads/x HEAD", "doesn't run git update-ref"),
			bash(rl, P, "git config --local x y", "doesn't run git config"),
			bash(rl, P, "git config --unset x", "doesn't run git config"),
			bash(rl, P, "git -C "+P+" tag v1", "doesn't run git tag"),
			bash(rl, P, "git stash", "doesn't run git stash"),
			bash(rl, P, "git fetch", "doesn't run git fetch"),
			bash(rl, P, "git diff --output=internal/a.go", "doesn't run git diff"),
		)
	}
	check(t, f.Project, append(cases,
		// Roles without a profile keep their rules: the integrator commits and writes; the worker rules are unchanged.
		bash("integrator", P, "printf x > internal/a.go", ""),
		bash("integrator", P, "git branch x", ""),
		bash("integrator", P, "git config --local x y", ""),
		edit("integrator", P, "Write", P+"/docs/research/l", ""),
		bash("worker", f.L, "git branch x", ""),
		bash("worker", f.L, "git branch -D x", "workers don't run git branch -D"),
		bash("worker", f.L, "git log", ""),
		bash("advisor", P, "git commit -m x", "an advisor doesn't run git commit"),
		bash("advisor", P, "git diff", ""),
	))
}

// gitRead's forms that read only in some shapes (RV-83 #2).
func TestGitReadForms(t *testing.T) {
	for _, c := range []struct {
		cmd  string
		read bool
	}{
		{"status", true}, {"log --oneline -3", true}, {"show HEAD:x", true}, {"blame x", true}, {"grep x", true},
		{"ls-files", true}, {"rev-parse HEAD", true}, {"branch", true}, {"branch -a", true}, {"branch -vv", true},
		{"branch --show-current", true}, {"branch --contains HEAD", true}, {"branch -l t*", true},
		{"branch --sort=-committerdate", true}, {"branch x", false}, {"branch -d x", false}, {"branch -c x y", false},
		{"branch --set-upstream-to=o/x", false}, {"branch -f x HEAD", false},
		{"config --get a.b", true}, {"config --list", true}, {"config -l", true}, {"config a.b", true},
		{"config get a.b", true}, {"config a.b c", false}, {"config --local a.b c", false}, {"config set a.b c", false},
		{"config --add a.b c", false}, {"config --unset a.b", false}, {"config --get --unset a.b", false},
		{"remote", true}, {"remote -v", true}, {"remote get-url origin", true}, {"remote add o u", false},
		{"remote set-url o u", false}, {"tag", true}, {"tag -l v*", true}, {"tag v1", false}, {"tag -d v1", false},
		{"stash", false}, {"stash list", true}, {"stash show", true}, {"stash pop", false},
		{"worktree list", true}, {"worktree add x", false}, {"reflog", true}, {"reflog expire --all", false},
		{"diff --output=x", false}, {"log --output x", false},
		{"update-ref r HEAD", false}, {"symbolic-ref HEAD r", false}, {"fetch", false}, {"pull", false},
		{"gc", false}, {"notes add", false}, {"commit -m x", false}, {"add x", false},
	} {
		w := strings.Fields(c.cmd)
		if got := gitRead(w[0], w[1:]); got != c.read {
			t.Errorf("git %s: read %v, want %v", c.cmd, got, c.read)
		}
	}
}

// T-751: the owner's drive agent (OBOR_ROLE=driver) in its lane edits only there (no other worktree, the main checkout
// included), no protected file (its spec unlocks none), and never pushes, even when guards.allow_push names it; unlike
// a worker it commits on its branch (obor land commits nothing).
func TestDriverLaneBoundCommitsNoPush(t *testing.T) {
	t.Parallel()
	f := fixture(t)
	P, L := f.P, f.L
	f.Config(testkit.Set("protected_files", `["src/core/types.ts"]`), testkit.Set("guards.allow_push", `["pj-l-01-drive"]`))
	f.Write(L+"/src/core/types.ts", "protected\n")
	check(t, f.Project, []tc{
		edit("driver pj-l-01-drive", L, "Write", L+"/src/a/page.ts", ""),
		edit("driver pj-l-01-drive", L, "Write", P+"/STATUS.md", "is in another worktree of this repo ("+P+") — a driver edits only inside its lane"),
		edit("driver pj-l-01-drive", L, "Edit", L+"/src/core/types.ts", "is a protected file"),
		bash("driver pj-l-01-drive", L, "git commit -m page", ""),
		bash("driver pj-l-01-drive", L, "git add src/a/page.ts", ""),
		bash("driver pj-l-01-drive", L, "git push origin task/T-01", "git push"),
		bash("worker pj-l-01", L, "git commit -m page", "workers don't run git commit"),
	})
}

// T-796: triage has a role (OBOR_ROLE=triage) and the guard holds it to the role table's: git reads only, protected
// files locked, its docs/BUGS.md writable; the owner (no role) in the main checkout keeps its rules.
func TestTriageRoleGuard(t *testing.T) {
	t.Parallel()
	f := fixture(t)
	P := f.P
	f.Config(testkit.Set("protected_files", `["src/core/types.ts"]`))
	cfg, err := config.Load(f.Root)
	if err != nil {
		t.Fatal(err)
	}
	// T-801: triage is a kit default custom row, writing only docs/BUGS.md.
	if rc, ok := cfg.Role("triage"); !ok || !rc.Role.GitReadOnly || !rc.Role.Custom || rc.Role.EnvValue != "triage" ||
		!reflect.DeepEqual(rc.Role.WritePaths, []string{"docs/BUGS.md"}) {
		t.Fatalf("precondition: triage a custom role, git read-only, writes docs/BUGS.md, env triage: %+v", rc.Role)
	}
	check(t, f.Project, []tc{
		bash("triage", P, "git commit -m x", "a triage doesn't run git commit"),
		bash("triage", P, "git add docs/BUGS.md", "a triage doesn't run git add"),
		bash("triage", P, "git stash", "a triage doesn't run git stash"),
		bash("triage", P, "git log --oneline -30", ""),
		bash("triage", P, "git status --short", ""),
		edit("triage", P, "Write", P+"/src/core/types.ts", "src/core/types.ts is a protected file"),
		bash("triage", P, "printf x > src/core/types.ts", "src/core/types.ts is a protected file"),
		edit("triage", P, "Write", P+"/docs/BUGS.md", ""),
		edit("triage", P, "Write", P+"/docs/NOTES.md", "docs/NOTES.md is outside the triage write profile"),
		// The owner (no role) in the main checkout: unchanged.
		bash("-", P, "git commit -m x", ""),
		edit("-", P, "Write", P+"/src/core/types.ts", ""),
	})
}
