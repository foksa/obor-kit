package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"obor/internal/agents"
	"obor/internal/app/wire"
	"obor/internal/config"
	"obor/internal/help"
	"obor/internal/project"
	"obor/internal/roles"
)

// sendCmd is `obor send <project> "<text>"`, a lead's or the owner's: a request to the target project's serve
// (wire.MethodSend), which posts "[obor <this project>] <text> (reply: obor send <this project> "…")" to that project's
// lead's Claude session.
// <project> is a project this machine knows by name (obor projects), lead@<name> (the same: only a lead gets
// messages), or a path into an obor project. Serve's reply (SENT to …), or FAIL: the own project, an unknown
// name, not an obor project, serve down or its refusal (exit 1).
func sendCmd(args []string) int {
	if len(args) != 2 || args[0] == "" || strings.TrimSpace(args[1]) == "" {
		fmt.Fprintln(out, "FAIL "+help.Usage("send"))
		return 1
	}
	ctx, env := context.Background(), project.OSEnv
	return request(ctx, env, func(cfg *config.Config, id wire.Envelope) (wire.Req, error) {
		root, err := sendTarget(args[0], env)
		if err != nil {
			return nil, err
		}
		if project.Physical(root) == project.Physical(cfg.Root) {
			return nil, errors.New("obor send goes to another project's lead, not this one")
		}
		id.Root = root
		c := &wire.SendReq{Envelope: id, Text: args[1], From: cfg.ProjectName()}
		return c, nil
	})
}

// sendTarget is the root of the project target names: a known name (or lead@<name>, the same thing), or a path inside
// an obor project (its main checkout).
func sendTarget(target string, env project.Env) (string, error) {
	if st, err := os.Stat(target); err == nil && st.IsDir() {
		repo, err := project.Find(target)
		if err != nil || !project.Configured(repo.Root) {
			return "", fmt.Errorf("%s is not in an obor project (no .obor/config.json)", target)
		}
		return repo.Root, nil
	}
	name := strings.TrimPrefix(target, "lead@")
	if root, ok := project.RootOf(name, env); ok {
		return root, nil
	}
	if strings.ContainsRune(target, os.PathSeparator) || target == "." || target == ".." {
		return "", fmt.Errorf("%s is not a directory: obor send takes a known project name (obor projects) or a path "+
			"into the target project", target)
	}
	return "", unknownProject(name, env)
}

// unknownProject is the refusal of a name this machine knows no project by: the names it does know.
func unknownProject(name string, env project.Env) error {
	var known []string
	for _, p := range project.Projects(env) {
		known = append(known, p.Name)
	}
	if len(known) == 0 {
		return fmt.Errorf("no obor project named %s (known: none: obor serve and obor init register a project)", name)
	}
	return fmt.Errorf("no obor project named %s (known: %s)", name, strings.Join(known, ", "))
}

// briefCmd is `obor brief <role-agent> [--fresh] "<text>"`, the lead's or the owner's: the one way to brief a role
// agent. A request to obor serve (wire.MethodBrief) that its pane or session reads "[obor <caller>] <text>"
// once the agent is idle; once that agent has worked on it and is idle again, serve posts
// "[obor] <agent> finished (briefed <age> ago)" to the lead's session. --fresh is the same request with fresh set:
// serve admits and records the brief (as any brief; the reviewer's and the deep dive's merges are held), and
// only then stops a running agent and starts it clean, the text its first prompt. The CLI stops nothing: a refused
// brief leaves the agent running. Serve's reply (BRIEFED …), or FAIL: not a role agent, serve down or its refusal
// (exit 1).
func briefCmd(args []string) int {
	fresh, rest := false, make([]string, 0, len(args))
	for _, a := range args {
		if a == "--fresh" {
			fresh = true
			continue
		}
		rest = append(rest, a)
	}
	if len(rest) != 2 || rest[0] == "" || strings.TrimSpace(rest[1]) == "" {
		fmt.Fprintln(out, "FAIL "+help.Usage("brief"))
		return 1
	}
	agent, text := rest[0], rest[1]
	ctx, env := context.Background(), project.OSEnv
	_, cfg, rc := load()
	if rc != 0 {
		fmt.Fprintln(out, noConfig)
		return 1
	}
	if !roleAgent(cfg, agent) {
		fmt.Fprintf(out, "FAIL %s is no role agent of this project (reviewer, deep_dive, advisor …; not the lead, the integrator or a worker)\n", agent)
		return 1
	}
	reply, err := brief(ctx, cfg, agent, text, fresh, env)
	if err != nil {
		fmt.Fprintf(out, "FAIL %v\n", err)
		return 1
	}
	fmt.Fprintln(out, reply)
	return 0
}

// brief is the brief request for agent (text the line its pane or session reads; fresh: serve starts it fresh on it);
// agent must be a role's agent other than the lead's, the integrator's and a worker's (reviewer, deep_dive, advisor,
// an agents entry …).
func brief(ctx context.Context, cfg *config.Config, agent, text string, fresh bool, env project.Env) (string, error) {
	cwd, _ := os.Getwd()
	r := &wire.BriefReq{Envelope: caller(env, cwd, cfg), To: agent, Text: text, Fresh: fresh}
	return wire.Verdict(ctx, wire.SocketPath(cfg.Root), r)
}

// roleAgent: agent is an agent of cfg (a role's or an agents entry) that serve sends no jobs to: not the lead, the
// integrator or a lane's agent (a worker, a drive).
func roleAgent(cfg *config.Config, agent string) bool {
	info, ok := cfg.AgentLookup(agent, "")
	return ok && info.Role != "lead" && info.Role != "integrator" && !roles.LaneBound(info.Role)
}

// poster posts serve's lines and job wakes into an agent's session whose kind has an inbox (app.Poster):
// the session's inbox socket as its SessionStart hook recorded it in the project at root (the record of the agent's
// slot), the kind's lines (agents.Kind.Deliver). Nothing is typed into a pane.
type poster struct{ root string }

// postWait bounds one post: the dial, the write, and the wait for the session to close its end after reading.
const postWait = 3 * time.Second

// Post posts line to agent's session as kind posts it (the pane's kind): the record read, the kind's
// lines from it, one dial, the lines written, then close. began: a byte of it went out (a refusal before is safe to
// retry). No ack comes back.
func (p poster) Post(ctx context.Context, kind, agent, line string) (began bool, err error) {
	k, ok := agents.Lookup(kind)
	if !ok || !k.Caps().Inbox {
		return false, fmt.Errorf("%s: kind %s posts through no inbox", agent, kind)
	}
	slot, err := slotOf(p.root, agent)
	if err != nil {
		return false, fmt.Errorf("no session recorded for %s: %w", agent, err)
	}
	f := project.InboxFile(p.root, slot)
	rec, err := os.ReadFile(f)
	if err != nil {
		return false, fmt.Errorf("no session recorded for %s: %w", agent, err)
	}
	d, ok := k.Deliver(line, rec)
	if !ok || d.Typed {
		return false, fmt.Errorf("no session recorded for %s: %s names no socket", agent, f)
	}
	ctx, cancel := context.WithTimeout(ctx, postWait)
	defer cancel()
	var dialer net.Dialer
	c, err := dialer.DialContext(ctx, "unix", d.Socket)
	if err != nil {
		return false, err
	}
	defer c.Close()
	deadline, _ := ctx.Deadline()
	_ = c.SetDeadline(deadline)
	if n, err := c.Write([]byte(strings.Join(d.Lines, "\n") + "\n")); err != nil {
		return n > 0, err
	}
	// Half-close and let the session read to the end before the connection goes: it closes its end once read (a
	// silent session: the deadline ends the wait; the lines are written either way).
	if uc, ok := c.(*net.UnixConn); ok {
		_ = uc.CloseWrite()
		_, _ = io.Copy(io.Discard, c)
	}
	return true, nil
}

// slotOf is the slot agent holds in the project at root, its inbox record's key (project.AgentSlot under the config as
// it is now: a rename moves no record); an error when agent holds none there.
func slotOf(root, agent string) (string, error) {
	cfg, err := config.Load(root)
	if err != nil {
		return "", err
	}
	slot := project.AgentSlot(cfg, agent)
	if slot == "" {
		return "", fmt.Errorf("%s holds no slot of the project at %s", agent, root)
	}
	return slot, nil
}

// inboxed is app.Options.Inboxed for the project at root: agent's inbox record names a socket that is there (a session
// that ended removed it; a crashed one's is refused at the dial and retried), and the session that wrote it.
func inboxed(root string) func(agent string) (string, bool) {
	return func(agent string) (string, bool) {
		slot, err := slotOf(root, agent)
		if err != nil {
			return "", false
		}
		in, err := project.ReadInbox(root, slot)
		if err != nil {
			return "", false
		}
		_, err = os.Stat(in.Socket)
		return in.Session, err == nil
	}
}
