package guard

import (
	"maps"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"mvdan.cc/sh/v3/syntax"

	"obor/internal/agents"
	"obor/internal/project"
	"obor/internal/roles"
)

// Bash commands are parsed with mvdan.cc/sh/v3/syntax and walked statement by statement, in source order:
//   - `cd [-L|-P|-e|-@]... [--] <dir>` moves the emulated cwd; ( … ), pipeline elements, `… &`, $( … ), <( … ),
//     function bodies and nested shells get their own cwd scope (as the shell runs them in a subshell).
//   - words are expanded: quotes removed, variables assigned earlier in the command (X=… / export X=…) or set in the
//     environment substituted (unquoted ones split on blanks); anything else stays as its source text.
//   - command substitutions are walked wherever they are, double-quoted ones and heredoc bodies included.
//   - nested commands: `bash|sh|zsh|dash|ksh … -c '…'` and `eval …` (any depth up to maxDepth), the wrappers (env,
//     sudo, xargs, time, command, exec, nice, nohup, timeout, stdbuf: each one's valueOpts take a value, any other
//     option is a flag; git/pkill/killall among the skipped words, or anywhere after a flag, is checked as a
//     command), find … -exec/-execdir/-ok/-okdir <cmd> ;, functions defined in the command.
//   - a command word the guard can't resolve statically (an unknown $X / ${…}, $( … ), `…`, $(( … )), a glob or brace
//     pattern; also after a wrapper and in a nested string) denies only when a later literal word of its command
//     (incl. the commands a nested string came from) is a git write verb (gitWrites) or pkill/killall, or the word is
//     a pattern that can be pkill/killall; words inside a substitution don't count (they are walked on their own).
//     Nothing is evaluated (no environment reads beyond the above).
//   - git -C / --git-dir / --work-tree / GIT_DIR= / GIT_WORK_TREE= name the repo for the main-checkout rule.
//   - writers: > >> &> >| <> targets, tee, rm, mv and cp targets (incl. <dir>/<source basename>), sed -i, git
//     checkout/restore paths (non-workers; workers can't run them at all): each goes through checkWrite, the same
//     path policy as the editor tools (protected files, the role's write profile, a final symlink at its target).
//   - a GitReadOnly role runs only the git subcommands gitRead classifies as reads (RV-83 #2).
//   - load runs of tests (T-543, every role): `go test` with -count > 1 or -race, a *.test binary with -test.count > 1
//     or -test.race, sibling backgrounded test commands, or a literal fan-out above one under xargs / parallel.
//
// Known bypasses (accidents, not an adversary): aliases and functions from the user's shell or git config (also
// git -c alias.<x>=…); scripts run from a file (they are not read: test scripts contain git commits of their
// fixtures); variables set by a command's output or by an earlier Bash call; interpreters (python, node, …) and
// other writers not listed above; commands the parser rejects (the statements before the syntax error are still
// checked); commands fed on stdin (`echo git push | sh`, `echo git | xargs -I X X push`) and an unresolved command
// word whose verb is itself unresolved or hidden in a substitution — the git pre-push hook is the backstop for pushes.

const maxDepth = 8

type shell struct {
	g      *guard
	src    string
	cwd    string
	vars   map[string]*string // nil = assigned a value we cannot know
	depth  int
	ctx    []string // the words of the commands a nested string came from (sh -c, eval, find -exec)
	bg     bool     // this scope runs in the background (an & of an enclosing statement; T-543)
	par    bool     // this command runs under a parallel fan-out (xargs -P, parallel; T-543)
	bgUnit *bool    // whether this background sibling contains a test command
}

// script checks every statement of src in a new shell scope (a child process: its cd and variables stay inside).
func (g *guard) script(src, cwd string, depth int) {
	s := &shell{g: g, cwd: cwd, vars: map[string]*string{}, depth: depth}
	s.run(src)
}

// run parses src and walks it in this shell (eval): its cd and variables apply here.
func (s *shell) run(src string) {
	if s.depth > maxDepth {
		return
	}
	saved := s.src
	s.src = src
	p := syntax.NewParser(syntax.Variant(syntax.LangBash))
	var stmts []*syntax.Stmt
	for st, err := range p.StmtsSeq(strings.NewReader(src)) {
		if err != nil {
			break
		}
		stmts = append(stmts, st)
	}
	s.stmts(stmts)
	s.src = saved
}

// nested checks src (sh -c's string) in a new shell scope run in cwd; W, the command it came from, joins
// its context.
func (s *shell) nested(src, cwd string, W []string) {
	c := &shell{g: s.g, cwd: cwd, vars: map[string]*string{}, depth: s.depth + 1, ctx: s.within(W), bg: s.bg, par: s.par}
	c.run(src)
}

// within is the context of a string nested in command W.
func (s *shell) within(W []string) []string { return append(append([]string{}, s.ctx...), W...) }

func (s *shell) clone() *shell {
	vars := make(map[string]*string, len(s.vars))
	for k, v := range s.vars {
		vars[k] = v
	}
	c := *s
	c.vars = vars
	return &c
}

func (s *shell) stmts(list []*syntax.Stmt) {
	bgSiblings := 0
	for _, st := range list {
		if st.Background {
			unit := false
			c := s.clone()
			c.bgUnit = &unit
			c.stmt(st)
			if unit {
				bgSiblings++
				if bgSiblings >= 2 {
					deny(manyTestsDeny)
				}
			}
			continue
		}
		if isWait(st, s) {
			bgSiblings = 0
		}
		s.stmt(st)
	}
}

// isWait reports a literal wait command. A wait ends the interval in which background siblings overlap.
func isWait(st *syntax.Stmt, s *shell) bool {
	ce, ok := st.Cmd.(*syntax.CallExpr)
	if !ok || len(ce.Args) == 0 || len(ce.Assigns) != 0 {
		return false
	}
	w := s.fields(ce.Args[0])
	return len(w) == 1 && w[0] == "wait"
}

func (s *shell) stmt(st *syntax.Stmt) {
	if st.Background || st.Coprocess || st.Disown {
		s = s.clone()
		s.bg = true
	}
	if ce, ok := st.Cmd.(*syntax.CallExpr); ok || st.Cmd == nil {
		s.redirs(st.Redirs)
		if ok {
			s.call(ce)
		}
		return
	}
	s.command(st.Cmd)
	s.redirs(st.Redirs)
}

// redirs: output redirect targets are writes; command substitutions anywhere in them (and in heredoc bodies) run.
func (s *shell) redirs(rs []*syntax.Redirect) {
	for _, r := range rs {
		switch r.Op {
		case syntax.RdrOut, syntax.AppOut, syntax.RdrClob, syntax.RdrAll, syntax.AppAll, syntax.RdrAllClob,
			syntax.AppAllClob, syntax.RdrInOut:
			if r.Word != nil {
				t := strings.Join(s.fields(r.Word), " ")
				if strings.Trim(t, "0123456789") != "" || t == "" {
					s.g.checkWriteArg(t, s.cwd)
				}
			}
		}
		if r.Word != nil {
			s.substs(r.Word)
		}
		if r.Hdoc != nil {
			s.substs(r.Hdoc)
		}
	}
}

// substs walks the command and process substitutions inside node, each in its own scope.
func (s *shell) substs(node syntax.Node) {
	if node == nil {
		return
	}
	syntax.Walk(node, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.CmdSubst:
			s.clone().stmts(n.Stmts)
			return false
		case *syntax.ProcSubst:
			s.clone().stmts(n.Stmts)
			return false
		}
		return true
	})
}

func (s *shell) command(cmd syntax.Command) {
	switch c := cmd.(type) {
	case *syntax.Subshell:
		s.clone().stmts(c.Stmts)
	case *syntax.Block:
		s.stmts(c.Stmts)
	case *syntax.BinaryCmd:
		if c.Op == syntax.Pipe || c.Op == syntax.PipeAll {
			s.clone().stmt(c.X)
			s.clone().stmt(c.Y)
			return
		}
		s.stmt(c.X)
		s.stmt(c.Y)
	case *syntax.IfClause:
		for ; c != nil; c = c.Else {
			s.stmts(c.Cond)
			s.stmts(c.Then)
		}
	case *syntax.WhileClause:
		s.stmts(c.Cond)
		s.stmts(c.Do)
	case *syntax.ForClause:
		s.substs(c.Loop)
		s.stmts(c.Do)
	case *syntax.CaseClause:
		s.substs(c.Word)
		for _, item := range c.Items {
			for _, p := range item.Patterns {
				s.substs(p)
			}
			s.stmts(item.Stmts)
		}
	case *syntax.FuncDecl:
		s.clone().stmt(c.Body)
	case *syntax.TimeClause:
		if c.Stmt != nil {
			s.stmt(c.Stmt)
		}
	case *syntax.CoprocClause:
		s.clone().stmt(c.Stmt)
	case *syntax.DeclClause:
		for _, a := range c.Args {
			s.assign(a)
		}
		for _, a := range c.Args {
			s.substs(a)
		}
	default:
		s.substs(cmd)
	}
}

// assign records X=value (a literal we can expand) for later words; anything else makes X unknown.
func (s *shell) assign(a *syntax.Assign) {
	if a.Name == nil || a.Naked {
		return
	}
	if a.Array != nil || a.Index != nil || a.Value == nil && !a.Append {
		if a.Value == nil && a.Array == nil && a.Index == nil {
			empty := ""
			s.vars[a.Name.Value] = &empty
			return
		}
		s.vars[a.Name.Value] = nil
		return
	}
	v, ok := s.literal(a.Value)
	if !ok {
		s.vars[a.Name.Value] = nil
		return
	}
	if a.Append {
		old, known := s.param(a.Name.Value)
		if !known {
			s.vars[a.Name.Value] = nil
			return
		}
		v = old + v
	}
	s.vars[a.Name.Value] = &v
}

// call is one simple command: assignments, then the words (prefix assignments only apply to the command's env).
func (s *shell) call(ce *syntax.CallExpr) {
	env := map[string]string{}
	if len(ce.Args) == 0 {
		for _, a := range ce.Assigns {
			s.assign(a)
		}
	} else {
		for _, a := range ce.Assigns {
			if a.Name != nil && a.Value != nil {
				if v, ok := s.literal(a.Value); ok {
					env[a.Name.Value] = v
				}
			}
		}
	}
	var words []string
	for _, w := range ce.Args {
		words = append(words, s.fields(w)...)
	}
	s.segment(words, env)
	for _, a := range ce.Assigns {
		s.substs(a)
	}
	for _, w := range ce.Args {
		s.substs(w)
	}
}

// ---- words -----------------------------------------------------------------------------------------------------

func (s *shell) source(n syntax.Node) string {
	a, b := int(n.Pos().Offset()), int(n.End().Offset())
	if a < 0 || b > len(s.src) || a > b {
		return ""
	}
	return s.src[a:b]
}

// param is the value of $name: assigned earlier in this command, else the environment (not PWD/OLDPWD: the hook's
// are not the command's). ok false when unknown.
func (s *shell) param(name string) (string, bool) {
	if v, seen := s.vars[name]; seen {
		if v == nil {
			return "", false
		}
		return *v, true
	}
	if name == "PWD" || name == "OLDPWD" || name == "" || strings.Trim(name, "0123456789@*#?$!-_") == "" {
		return "", false
	}
	return s.g.env(name)
}

func simpleParam(p *syntax.ParamExp) bool {
	return p.Param != nil && p.Flags == nil && !p.Excl && !p.Length && !p.Width && !p.IsSet &&
		p.NestedParam == nil && p.Index == nil && len(p.Modifiers) == 0 && p.Slice == nil && p.Repl == nil &&
		p.Names == 0 && p.Exp == nil
}

// literal is a word's value without field splitting (an assignment's value); ok false when a part is unknown.
func (s *shell) literal(w *syntax.Word) (string, bool) {
	var b strings.Builder
	ok := true
	var walk func(parts []syntax.WordPart, quoted bool)
	walk = func(parts []syntax.WordPart, quoted bool) {
		for _, part := range parts {
			switch p := part.(type) {
			case *syntax.Lit:
				b.WriteString(unescape(p.Value, quoted))
			case *syntax.SglQuoted:
				b.WriteString(p.Value)
			case *syntax.DblQuoted:
				walk(p.Parts, true)
			case *syntax.ParamExp:
				if v, known := s.param(paramName(p)); known && simpleParam(p) {
					b.WriteString(v)
				} else {
					ok = false
				}
			default:
				ok = false
			}
		}
	}
	walk(w.Parts, false)
	return b.String(), ok
}

func paramName(p *syntax.ParamExp) string {
	if p.Param == nil {
		return ""
	}
	return p.Param.Value
}

// fields expands w into the words the command sees: quotes removed, known variables substituted (unquoted ones split
// on blanks); unknown expansions, command substitutions and arithmetic stay as their source text, as the bash
// hook's tokenizer saw them.
func (s *shell) fields(w *syntax.Word) []string {
	var out []string
	var cur strings.Builder
	have := false
	flush := func() {
		out = append(out, cur.String())
		cur.Reset()
		have = false
	}
	var walk func(parts []syntax.WordPart, quoted bool)
	walk = func(parts []syntax.WordPart, quoted bool) {
		for _, part := range parts {
			switch p := part.(type) {
			case *syntax.Lit:
				cur.WriteString(unescape(p.Value, quoted))
				have = true
			case *syntax.SglQuoted:
				cur.WriteString(p.Value)
				have = true
			case *syntax.DblQuoted:
				have = true
				walk(p.Parts, true)
			case *syntax.ParamExp:
				v, known := s.param(paramName(p))
				switch {
				case !known || !simpleParam(p):
					cur.WriteString(s.source(p))
					have = true
				case quoted:
					cur.WriteString(v)
				default:
					pieces := strings.Fields(v)
					if len(v) > 0 && isBlank(v[0]) && have {
						flush()
					}
					for i, pc := range pieces {
						if i > 0 {
							flush()
						}
						cur.WriteString(pc)
						have = true
					}
					if len(pieces) > 0 && isBlank(v[len(v)-1]) {
						flush()
					}
				}
			default:
				cur.WriteString(s.source(part))
				have = true
			}
		}
	}
	walk(w.Parts, false)
	if have {
		flush()
	}
	return out
}

func isBlank(c byte) bool { return c == ' ' || c == '\t' || c == '\n' }

// unescape removes the backslashes the shell removes: unquoted any \c → c; in double quotes \" \\ \$ \` only;
// a backslash-newline goes in both.
func unescape(v string, quoted bool) string {
	if !strings.Contains(v, `\`) {
		return v
	}
	var b strings.Builder
	for i := 0; i < len(v); i++ {
		c := v[i]
		if c != '\\' || i+1 >= len(v) {
			b.WriteByte(c)
			continue
		}
		n := v[i+1]
		switch {
		case n == '\n':
			i++
		case !quoted || n == '"' || n == '\\' || n == '$' || n == '`':
			b.WriteByte(n)
			i++
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// ---- one simple command ----------------------------------------------------------------------------------------

// headless denies an agent CLI run headless (T-470, owner 2026-10-27), for every role, by its argv structure (an
// accident guard: a prompt word is quoted, so it is no word of its own), as its kind names the forms h
// (agents.Kind.Headless; name is the program typed, one of h.Programs): a subcommand (codex exec, opencode run), one
// also as any later positional word (Anywhere); a print flag -p, --print or a short cluster holding p (-cp, -pc) before
// `--` (claude; it has no -p-shaped option values). Words after `--` and option values never count. Accepted, not fixed (owner/lead 2026-10-28, RV-51 #1): the
// alias after a prompt word (`codex "hello" e hi`); `env -S` separators the shell parser reads differently (`\_`).
func headless(h agents.Headless, name string, args []string) {
	if sub := subcommand(args, h.ValueOpts); oneOf(sub, h.Subcommands...) {
		deny(name + " " + sub + " runs an agent headless; agents run in herdr panes")
	}
	for _, w := range positionals(args, h.ValueOpts) {
		if oneOf(w, h.Anywhere...) {
			deny(name + " " + w + " runs an agent headless; agents run in herdr panes")
		}
	}
	if !h.PrintFlag {
		return
	}
	for _, a := range args {
		if a == "--" {
			return
		}
		a, _, _ = strings.Cut(a, "=")
		if a == "--print" || len(a) > 1 && a[0] == '-' && a[1] != '-' && strings.Contains(a, "p") &&
			strings.Trim(a[1:], "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ") == "" {
			deny(name + " " + a + " runs an agent headless; agents run in herdr panes")
		}
	}
}

// subcommand is the first word of args that is neither an option nor an option's value; "" when there is none.
func subcommand(args, valueOpts []string) string {
	if w := positionals(args, valueOpts); len(w) > 0 {
		return w[0]
	}
	return ""
}

// positionals are the words of args before `--` that are neither an option nor an option's value (valueOpts take the
// next word; in a short cluster the first letter that takes a value takes the rest of the word, or the next word
// when it is the last letter).
func positionals(args, valueOpts []string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--":
			return out
		case strings.HasPrefix(a, "--"):
			if oneOf(a, valueOpts...) {
				i++
			}
		case len(a) > 1 && a[0] == '-':
			for x := 1; x < len(a); x++ {
				if oneOf("-"+a[x:x+1], valueOpts...) {
					if x == len(a)-1 {
						i++
					}
					break
				}
			}
		default:
			out = append(out, a)
		}
	}
	return out
}

// ---- load runs of tests (T-543) -------------------------------------------------------------------------------------

// Two load-run rules for every role (in specs, a prose rule did not hold): a go test / test binary that runs the tests
// many times or with the race detector, and two or more test commands at once. Each deny line names the rule and the
// allowed form.
const (
	loadRunDeny = "a load run of tests (`-count` > 1, `-race`) is denied — one plain `go test <pkgs>` run; the gate and " +
		"nightly run the rest"
	manyTestsDeny = "two or more test commands at once (a background &, xargs -P, parallel) are a load run — one plain " +
		"`go test <pkgs>` run; the gate and nightly run the rest"
)

// goTest checks a `go` command: `go test` with -count > 1 or -race is a load run (T-543). -test.count / -test.race are
// checked too: go test passes them to the test binary, so they run it many times just the same.
func (s *shell) goTest(W []string, k int) {
	args := W[k+1:]
	i := goSub(args)
	if i < 0 || args[i] != "test" {
		return
	}
	f := args[i+1:]
	f = testFlags(f)
	if overCount(f, "count") || overCount(f, "test.count") || raceOn(f, "race") || raceOn(f, "test.race") {
		deny(loadRunDeny)
	}
	s.testRun()
}

// testBinary checks a compiled test binary (`go test -c`): -test.count > 1 or -test.race is a load run (T-543).
func (s *shell) testBinary(args []string) {
	args = testFlags(args)
	if overCount(args, "test.count") || raceOn(args, "test.race") {
		deny(loadRunDeny)
	}
	s.testRun()
}

// testRun denies test commands under a literal multi-worker fan-out; stmts handles sibling background statements.
func (s *shell) testRun() {
	if s.par {
		deny(manyTestsDeny)
	}
	if s.bgUnit != nil {
		*s.bgUnit = true
	}
}

// testFlags keeps test flag values from being mistaken for flags and stops at the test binary's argument boundary.
func testFlags(args []string) []string {
	valueFlags := map[string]bool{"-run": true, "-bench": true, "-benchtime": true, "-timeout": true,
		"-cpu": true, "-parallel": true, "-coverpkg": true, "-coverprofile": true, "-outputdir": true,
		"-exec": true, "-vet": true, "-tags": true, "-overlay": true, "-mod": true, "-modfile": true,
		"-fuzz": true, "-fuzztime": true, "-fuzzminimizetime": true, "-list": true, "-skip": true,
		"-asmflags": true, "-buildmode": true, "-compiler": true, "-gccgoflags": true, "-gcflags": true,
		"-installsuffix": true, "-ldflags": true, "-o": true, "-p": true, "-pkgdir": true}
	var flags []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "-args" {
			break
		}
		flags = append(flags, a)
		option := strings.TrimPrefix(a, "-test.")
		if valueFlags[a] || valueFlags["-"+option] && strings.HasPrefix(a, "-test.") {
			if !strings.Contains(a, "=") && i+1 < len(args) {
				i++
			}
		}
	}
	return flags
}

// goSub is the index of a go command's subcommand in args (the first word that is not an option; -C takes a value), or
// -1 when there is none.
func goSub(args []string) int {
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "-C":
			i++
		case strings.HasPrefix(a, "-"):
		default:
			return i
		}
	}
	return -1
}

// overCount: args has -name=N or -name N (--name too) with N > 1.
func overCount(args []string, name string) bool {
	n, found, ok := flagInt(args, name)
	return found && ok && n > 1
}

// flagInt is the value of the last -name / --name in args (-name=V or -name V; Go's flag package keeps the last): found
// false when there is none, ok false when the value is not a plain integer (an expansion the guard cannot read).
func flagInt(args []string, name string) (n int, found, ok bool) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		var v string
		switch {
		case strings.HasPrefix(a, "--"+name+"="):
			v = a[len(name)+3:]
		case strings.HasPrefix(a, "-"+name+"="):
			v = a[len(name)+2:]
		case a == "--"+name || a == "-"+name:
			if i+1 >= len(args) {
				continue
			}
			i++
			v = args[i]
		default:
			continue
		}
		val, err := strconv.Atoi(v)
		n, found, ok = val, true, err == nil
	}
	return n, found, ok
}

// raceOn: args switches the race detector on (-name, or -name=true / -name=1); a later -name=false / -name=0 wins.
func raceOn(args []string, name string) bool {
	on := false
	for _, a := range args {
		var v string
		switch {
		case a == "-"+name || a == "--"+name:
			on = true
			continue
		case strings.HasPrefix(a, "--"+name+"="):
			v = a[len(name)+3:]
		case strings.HasPrefix(a, "-"+name+"="):
			v = a[len(name)+2:]
		default:
			continue
		}
		on = v != "false" && v != "0"
	}
	return on
}

// segment checks one simple command's words W, run in s.cwd. env holds its literal prefix assignments.
func (s *shell) segment(W []string, env map[string]string) {
	n, k := len(W), 0
	// Strip prefixes: assignments, shell keywords and wrappers (unwrap skips their options).
	inChild, par := false, false
loop:
	for k < n {
		a := W[k]
		switch {
		case isAssignment(a):
			k++
		case a == "!" || a == "{" || a == "}" || a == "if" || a == "then" || a == "else" || a == "elif" || a == "fi" ||
			a == "do" || a == "done" || a == "while" || a == "until":
			k++
		default:
			child, ok := wrappers[base(a)]
			if !ok {
				break loop
			}
			if child && !inChild {
				s, inChild = s.clone(), true
			}
			p := false
			k, p = s.unwrap(W, k, env)
			if p && !par {
				s, par = s.clone(), true
				s.par = true
			}
		}
	}
	if k >= n {
		return
	}
	s.resolved(W, k)
	g := s.g
	switch name := W[k][strings.LastIndexByte(W[k], '/')+1:]; name {
	case "git":
		s.git(W, k, s.cwd, env)
	case "gh":
		g.gh(W, k)
	case "pkill", "killall":
		deny(name + " kills by pattern and can hit other lanes' processes — stop only processes you started, by PID (kill <pid>)")
	case "cd":
		s.cd(W, k)
	case "bash", "sh", "zsh", "dash", "ksh":
		// Options until `--` / `-` / the first operand; with -c (alone or clustered) that operand is the script.
		cflag, i := false, k+1
	opts:
		for ; i < n; i++ {
			switch w := W[i]; {
			case w == "--" || w == "-":
				i++
				break opts
			case w == "-o" || w == "+o" || w == "-O" || w == "+O" || w == "--rcfile" || w == "--init-file":
				i++
			case strings.HasPrefix(w, "--"):
			case strings.HasPrefix(w, "-") && strings.Contains(w, "c"):
				cflag = true
			case strings.HasPrefix(w, "-") || strings.HasPrefix(w, "+"):
			default:
				break opts
			}
		}
		if cflag && i < n {
			s.nested(W[i], s.cwd, W)
		}
	case "eval":
		if s.depth < maxDepth {
			saved := s.ctx
			s.depth, s.ctx = s.depth+1, s.within(W)
			s.run(strings.Join(W[k+1:], " "))
			s.depth, s.ctx = s.depth-1, saved
		}
	case "go":
		s.goTest(W, k)
	case "find":
		for i := k + 1; i < n; i++ {
			if !oneOf(W[i], "-exec", "-execdir", "-ok", "-okdir") {
				continue
			}
			j := i + 1
			for j < n && W[j] != ";" && W[j] != "+" {
				j++
			}
			if j > i+1 {
				c := s.clone()
				c.ctx = s.within(W)
				c.segment(W[i+1:j], map[string]string{})
			}
			i = j
		}
	case "cp":
		s.copyTargets(W, k, name)
	case "mv":
		// Every operand is written (a source is removed), but a final directory only receives: copyTargets checks
		// <dir>/<basename>.
		for i := k + 1; i < n; i++ {
			if !strings.HasPrefix(W[i], "-") && !(i == n-1 && s.isDir(W[i])) {
				g.checkWriteArg(W[i], s.cwd)
			}
		}
		s.copyTargets(W, k, name)
	case "tee", "rm":
		for i := k + 1; i < n; i++ {
			if !strings.HasPrefix(W[i], "-") {
				g.checkWriteArg(W[i], s.cwd)
			}
		}
	case "sed":
		s.sed(W, k)
	default:
		if strings.HasSuffix(name, ".test") {
			s.testBinary(W[k+1:])
		}
		if kind, ok := agents.ByProgram(name); ok {
			headless(kind.Headless(), name, W[k+1:])
		}
	}
}

// gitWrites: the git verbs a worker may not run (git); after an unresolved command word they deny for every role.
var gitWrites = []string{"commit", "merge", "rebase", "reset", "checkout", "switch", "restore", "stash", "cherry-pick",
	"revert", "push", "tag", "worktree", "clean", "am", "apply"}

// workerIndexWrites: git verbs that write the index; denied for workers only. Not in gitWrites: add, rm and mv are
// common words, so a lead's "$X rm file" must stay allowed after an unresolved command word.
var workerIndexWrites = []string{"add", "rm", "mv"}

// resolved: the command word W[k] is unresolved (see unresolved) and a later word of the command, or of one a nested
// string came from, is a git write verb or pkill/killall — or W[k] is a pattern that can be pkill/killall — deny: it
// may run git with that verb or a kill by pattern. Otherwise ("$(git rev-parse --show-toplevel)"/x.sh, $EDITOR .git…)
// allow.
func (s *shell) resolved(W []string, k int) {
	cmd := W[k]
	if !unresolved(cmd) {
		return
	}
	hit := ""
	if !strings.ContainsAny(cmd, "$`") {
		for _, name := range []string{"pkill", "killall"} {
			m, _ := filepath.Match(base(cmd), name)
			for _, t := range strings.FieldsFunc(cmd, func(r rune) bool { return r == '{' || r == ',' || r == '}' || r == '/' }) {
				m = m || t == name
			}
			if m {
				hit = name
			}
		}
	}
	for _, w := range append(append([]string{}, s.ctx...), W[k+1:]...) {
		if hit == "" && (oneOf(w, gitWrites...) || oneOf(base(w), "pkill", "killall")) {
			hit = w
		}
	}
	if hit != "" {
		deny("the command word " + cmd + " can't be resolved statically (an expansion, a substitution or a pattern) and " +
			hit + " is in the command, so the guard can't tell what runs — write the command name literally")
	}
}

// unresolved: after quote removal and known-variable substitution, w still holds an expansion ($X, ${…}, $( … ),
// $(( … ))), a backquote substitution, a process substitution, or a glob or brace pattern.
func unresolved(w string) bool {
	if strings.ContainsAny(w, "$`*?") || strings.Contains(w, "<(") || strings.Contains(w, ">(") {
		return true
	}
	if i := strings.IndexByte(w, '['); i >= 0 && strings.IndexByte(w[i+1:], ']') >= 0 {
		return true
	}
	if i := strings.IndexByte(w, '{'); i >= 0 {
		if j := strings.IndexByte(w[i:], '}'); j > 0 {
			return strings.Contains(w[i:i+j], ",") || strings.Contains(w[i:i+j], "..")
		}
	}
	return false
}

func isAssignment(a string) bool {
	i := strings.IndexByte(a, '=')
	if i <= 0 {
		return false
	}
	name := a[:i]
	if c := name[0]; !(c == '_' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z') {
		return false
	}
	return strings.Trim(name, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_") == ""
}

func base(w string) string { return w[strings.LastIndexByte(w, '/')+1:] }

// ---- wrappers: commands that run another command -----------------------------------------------------------------

// wrappers run the command after their options; true: in a child process (its cd stays inside).
var wrappers = map[string]bool{"env": true, "sudo": true, "xargs": true, "time": true, "nice": true, "nohup": true,
	"timeout": true, "stdbuf": true, "parallel": true, "command": false, "exec": false}

// valueOpts: the options of each wrapper that take the next word as their value (sudo -u USER, env -u NAME, xargs -I R,
// timeout -s SIG, nice -n N, …); env -C and sudo -D name the nested command's cwd. Any other option is a flag.
var valueOpts = map[string][]string{
	"sudo":    {"-u", "-g", "-C", "-D", "-h", "-p", "-r", "-t", "-U"},
	"env":     {"-u", "-C", "-S"},
	"xargs":   {"-I", "-n", "-s", "-L", "-P", "-d", "-E", "--max-procs", "--max-args"},
	"timeout": {"-s", "-k"},
	"stdbuf":  {"-i", "-o", "-e"},
	"nice":    {"-n"},
	"exec":    {"-a"},
	"time":    {"-f", "-o"},
	"parallel": {"-j", "--jobs", "-S", "--sshlogin", "-I", "--replace", "-n", "--max-args", "-L", "--max-lines", "-a",
		"--arg-file", "-d", "--delimiter", "-E", "--eof", "--colsep", "--tmpdir", "--results"},
}

// unwrap skips wrapper W[k]'s options (to --, or the first word not starting with -), then timeout's duration and
// NAME=VALUE words (into env), and returns the index of the nested command, and whether the wrapper fans out in
// parallel (xargs -P / --max-procs, parallel; T-543). A flag may still take a value the guard
// doesn't know of, and a value may be the command after all, so net checks git/pkill/killall among the skipped
// words, and after a flag among all later words, as commands (fail closed).
func (s *shell) unwrap(W []string, k int, env map[string]string) (int, bool) {
	name, i, flag, par := base(W[k]), k+1, false, false
opts:
	for ; i < len(W) && strings.HasPrefix(W[i], "-"); i++ {
		if fanoutOption(name, W, i) {
			par = true
		}
		switch a := W[i]; {
		case a == "--":
			i++
			break opts
		case oneOf(a, valueOpts[name]...):
			i++
			if (name == "env" && a == "-C" || name == "sudo" && a == "-D") && i < len(W) {
				if p, ok := s.g.phys(W[i], s.cwd); ok {
					s.cwd = p
				}
			}
		default:
			flag = true
		}
	}
	if name == "timeout" && i < len(W) {
		i++
	}
	to := min(i, len(W))
	if flag {
		to = len(W)
	}
	s.net(W, k+1, to, env)
	for ; i < len(W) && isAssignment(W[i]); i++ {
		key, val, _ := strings.Cut(W[i], "=")
		env[key] = val
	}
	return i, par
}

// fanoutOption recognizes only literal fan-out counts above one, plus xargs' unbounded zero.
func fanoutOption(name string, words []string, i int) bool {
	a := words[i]
	short, long := "", ""
	if name == "xargs" {
		short, long = "-P", "--max-procs"
	} else if name == "parallel" {
		short, long = "-j", "--jobs"
	} else {
		return false
	}
	var v string
	switch {
	case strings.HasPrefix(a, long+"="):
		v = strings.TrimPrefix(a, long+"=")
	case a == long || a == short:
		if i+1 < len(words) {
			v = words[i+1]
		}
	case strings.HasPrefix(a, short) && len(a) > len(short):
		v = strings.TrimPrefix(a, short)
	default:
		return false
	}
	n, err := strconv.Atoi(v)
	return err == nil && (n > 1 || name == "xargs" && n == 0)
}

// net: from each word W[from:to] (or word of one split as env -S splits it: env -S 'git "push"') that names git,
// pkill, killall or an agent CLI (headless), check the rest of W as a command in a child scope.
func (s *shell) net(W []string, from, to int, env map[string]string) {
	for j := from; j < to; j++ {
		f := s.splitS(W[j])
		for x := range f {
			if _, agent := agents.ByProgram(base(f[x])); agent || oneOf(base(f[x]), "git", "pkill", "killall") {
				c := s.clone()
				c.segment(append(append([]string{}, f[x:]...), W[j+1:]...), maps.Clone(env))
			}
		}
	}
}

// splitS splits an env -S string (also -S<string>, --split-string=<string>) as env does: blank-separated words,
// quotes removed, by the guard's own word parser; a string it rejects splits on blanks.
func (s *shell) splitS(v string) []string {
	if len(v) > 2 && strings.HasPrefix(v, "-S") {
		v = v[2:]
	} else if t, ok := strings.CutPrefix(v, "--split-string="); ok {
		v = t
	}
	c := s.clone()
	c.src = v
	var out []string
	p := syntax.NewParser(syntax.Variant(syntax.LangBash))
	if err := p.Words(strings.NewReader(v), func(w *syntax.Word) bool {
		out = append(out, c.fields(w)...)
		return true
	}); err != nil {
		return strings.Fields(v)
	}
	return out
}

// cd [-L|-P|-e|-@]... [--] <dir>; `cd -` (OLDPWD) and `cd` (HOME) leave the cwd alone.
func (s *shell) cd(W []string, k int) {
	n, i := len(W), k+1
args:
	for ; i < n; i++ {
		switch w := W[i]; {
		case w == "--":
			i++
			break args
		case w == "-":
			break args
		case strings.HasPrefix(w, "-") && strings.Trim(w[1:], "LPe@") != "":
			i = n
			break args
		case strings.HasPrefix(w, "-"):
		default:
			break args
		}
	}
	if i >= n || W[i] == "" || W[i] == "-" {
		return
	}
	if p, ok := s.g.phys(W[i], s.cwd); ok {
		s.cwd = p
	}
}

// copyTargets: the write targets of cp / mv — the -t / --target-directory dir, else the last operand.
// A directory target also gets <dir>/<basename of each source> checked (an existing dir, or any target with -t).
func (s *shell) copyTargets(W []string, k int, name string) {
	g, n := s.g, len(W)
	tdir, i := "", k+1
	var ops []string
opts:
	for ; i < n; i++ {
		switch a := W[i]; {
		case a == "--":
			i++
			break opts
		case a == "-t" || a == "--target-directory":
			i++
			if i < n {
				tdir = W[i]
			}
		case strings.HasPrefix(a, "--target-directory="):
			tdir = a[len("--target-directory="):]
		case a == "-S" || a == "--suffix":
			i++
		case strings.HasPrefix(a, "--"):
		case len(a) > 2 && a[0] == '-' && a[1] != '-' && strings.HasSuffix(a, "t"):
			i++
			if i < n {
				tdir = W[i]
			}
		case strings.HasPrefix(a, "-t") && len(a) > 2:
			tdir = a[2:]
		case strings.HasPrefix(a, "-") && len(a) > 1:
		default:
			ops = append(ops, a)
		}
	}
	ops = append(ops, W[min(i, n):]...)
	if len(ops) == 0 {
		return
	}
	if tdir == "" {
		if len(ops) < 2 {
			return
		}
		tdir = ops[len(ops)-1]
		ops = ops[:len(ops)-1]
		if !s.isDir(tdir) {
			g.checkWriteArg(tdir, s.cwd) // a file target; into a directory, the written path is <dir>/<basename>
			return
		}
	}
	for _, a := range ops {
		a = strings.TrimSuffix(a, "/")
		g.checkWriteArg(strings.TrimSuffix(tdir, "/")+"/"+filepath.Base("/"+a), s.cwd)
	}
}

// isDir: the path argument a (relative to the emulated cwd) is an existing directory.
func (s *shell) isDir(a string) bool {
	p, ok := s.g.phys(a, s.cwd)
	if !ok {
		return false
	}
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// sed: with -i / --in-place, the files are the non-option args minus the script when no -e/-f gave it.
func (s *shell) sed(W []string, k int) {
	n, ei, inplace := len(W), false, false
	var files []string
	for i := k + 1; i < n; i++ {
		switch a := W[i]; {
		case strings.HasPrefix(a, "--in-place"):
			inplace = true
		case a == "-i" && i+1 < n && W[i+1] == "":
			inplace = true
			i++ // BSD sed's empty backup suffix (sed -i '' …), not the script
		case a == "-e" || a == "-f" || a == "--expression" || a == "--file":
			ei = true
			i++
		case strings.HasPrefix(a, "--expression=") || strings.HasPrefix(a, "--file="):
			ei = true
		case strings.HasPrefix(a, "--"):
		case strings.HasPrefix(a, "-"):
			if strings.Contains(a[1:], "i") {
				inplace = true
			}
			if last := a[len(a)-1]; len(a) > 1 && (last == 'e' || last == 'f') {
				ei = true
				i++
			} else if strings.ContainsAny(a[1:], "ef") {
				ei = true
			}
		default:
			files = append(files, a)
		}
	}
	if !inplace {
		return
	}
	scriptSeen := ei
	for _, f := range files {
		if !scriptSeen {
			scriptSeen = true
			continue
		}
		s.g.checkWriteArg(f, s.cwd)
	}
}

// ---- git and gh ------------------------------------------------------------------------------------------------

// git <global opts> <sub> <args>: words W[k..] with W[k] = git, run in dir.
func (s *shell) git(W []string, k int, dir string, env map[string]string) {
	g, n := s.g, len(W)
	gitDir, workTree := env["GIT_DIR"], env["GIT_WORK_TREE"]
	i := k + 1
	for i < n {
		a := W[i]
		switch {
		case a == "-C":
			if i+1 < n {
				if p, ok := s.g.phys(W[i+1], dir); ok {
					dir = p
				}
			}
			i += 2
		case a == "--git-dir" || a == "--work-tree":
			if i+1 < n {
				if a == "--git-dir" {
					gitDir = W[i+1]
				} else {
					workTree = W[i+1]
				}
			}
			i += 2
		case strings.HasPrefix(a, "--git-dir="):
			gitDir = a[len("--git-dir="):]
			i++
		case strings.HasPrefix(a, "--work-tree="):
			workTree = a[len("--work-tree="):]
			i++
		case a == "-c" || a == "--namespace" || a == "--super-prefix" || a == "--config-env" || a == "--exec-path":
			i += 2
		case strings.HasPrefix(a, "-"):
			i++
		default:
			goto sub
		}
	}
	return
sub:
	sub := W[i]
	if sub == "push" {
		g.checkPush("git push")
	}

	if sub == "commit" {
		hit := false
	args:
		for j := i + 1; j < n; j++ {
			a := W[j]
			switch {
			case a == "--":
				break args
			case a == "--all":
				hit = true
			case a == "-m" || a == "-F" || a == "-C" || a == "-c" || a == "-t" || a == "--author" || a == "--date" ||
				a == "--message" || a == "--file" || a == "--template" || a == "--fixup" || a == "--squash" ||
				a == "--trailer" || a == "--cleanup" || a == "--reuse-message" || a == "--reedit-message":
				j++
			case strings.HasPrefix(a, "--"):
			case len(a) > 1 && a[0] == '-':
				for _, c := range a[1:] {
					if c == 'a' {
						hit = true
						break
					}
					if strings.ContainsRune("mFCct", c) {
						break
					}
				}
			}
		}
		if hit && g.isMain(dir, gitDir, workTree) {
			deny("git commit -a in the main checkout sweeps in everyone's uncommitted edits — git add the files you mean, then commit without -a/--all")
		}
	}

	if g.broken != nil && g.restricted && (oneOf(sub, gitWrites...) || oneOf(sub, workerIndexWrites...)) {
		deny(unreadable(g.broken))
	}
	if r, ok := roles.Lookup(g.role); ok && r.GitReadOnly && !gitRead(sub, W[i+1:]) {
		a := "a "
		if strings.ContainsRune("aeiou", rune(g.role[0])) {
			a = "an "
		}
		deny(a + g.role + " doesn't run git " + strings.Join(W[i:min(i+2, n)], " ") + " — it runs git reads only " +
			"(status, log, diff, show, blame, grep, plain git branch, config --get) and leaves its files uncommitted; the lead commits")
	}
	if g.role != "worker" {
		// Non-workers may check out / restore, but not over a protected file.
		if sub == "checkout" || sub == "restore" {
			for j := i + 1; j < n; j++ {
				a := W[j]
				switch {
				case a == "-s" || a == "--source" || a == "-b" || a == "-B" || a == "--orphan" || a == "--conflict":
					j++
				case a == "--" || strings.HasPrefix(a, "-"):
				default:
					g.checkWriteArg(a, dir)
				}
			}
		}
		return
	}
	switch {
	case oneOf(sub, gitWrites...) || oneOf(sub, workerIndexWrites...):
		deny("workers don't run git " + sub + " — leave changes uncommitted in the lane; the integrator commits and merges (status, diff, log, show are fine)")
	case sub == "branch":
		for _, a := range W[i+1:] {
			if a == "--delete" || a == "--move" || a == "--force" || a == "--copy" ||
				len(a) > 1 && a[0] == '-' && a[1] != '-' && strings.ContainsAny(a[1:], "dDmMfcC") {
				deny("workers don't run git branch " + a + " — leave branches alone; the lead and integrator manage them (plain git branch is fine)")
			}
		}
	}
}

// gitReads: git subcommands that only read (for a GitReadOnly role, RV-83 #2). branch, config, remote, tag, stash,
// worktree and reflog read in some forms only (gitRead); every other subcommand counts as a write.
var gitReads = []string{"status", "log", "diff", "show", "blame", "annotate", "grep", "ls-files", "ls-tree", "ls-remote",
	"rev-parse", "rev-list", "cat-file", "describe", "shortlog", "merge-base", "show-ref", "for-each-ref", "name-rev",
	"diff-tree", "diff-files", "diff-index", "range-diff", "whatchanged", "cherry", "check-ignore", "check-attr",
	"count-objects", "var", "help", "version"}

// gitRead: git <sub> <args> only reads. An --output option (diff, log, show write a file with it) is a write.
func gitRead(sub string, args []string) bool {
	for _, a := range args {
		if a == "--output" || strings.HasPrefix(a, "--output=") {
			return false
		}
	}
	pos := positionals(args, nil)
	switch sub {
	case "branch":
		return branchList(args)
	case "config":
		return configRead(args)
	case "remote":
		return len(pos) == 0 || oneOf(pos[0], "show", "get-url")
	case "tag":
		return len(args) == 0 || oneOf("-l", args...) || oneOf("--list", args...)
	case "stash":
		return len(pos) > 0 && oneOf(pos[0], "list", "show")
	case "worktree":
		return len(pos) > 0 && pos[0] == "list"
	case "reflog":
		return len(pos) == 0 || oneOf(pos[0], "show", "exists")
	}
	return oneOf(sub, gitReads...)
}

// branchList: git branch with no args, listing options only, or --list / -l with patterns.
func branchList(args []string) bool {
	list := false
	var pos []string
	for j := 0; j < len(args); j++ {
		a := args[j]
		switch {
		case a == "--list":
			list = true
		case oneOf(a, "--contains", "--no-contains", "--merged", "--no-merged", "--points-at", "--sort", "--format"):
			if j+1 < len(args) && !strings.HasPrefix(args[j+1], "-") {
				j++
			}
		case oneOf(a, "--all", "--remotes", "--verbose", "--show-current", "--ignore-case", "--no-color", "--color",
			"--column", "--no-column", "--abbrev", "--no-abbrev", "--omit-empty"):
		case strings.HasPrefix(a, "--"):
			k, _, _ := strings.Cut(a, "=")
			if !strings.Contains(a, "=") || !oneOf(k, "--contains", "--no-contains", "--merged", "--no-merged",
				"--points-at", "--sort", "--format", "--color", "--column", "--abbrev") {
				return false
			}
		case len(a) > 1 && a[0] == '-':
			if strings.Trim(a[1:], "arvli") != "" {
				return false
			}
			list = list || strings.Contains(a, "l")
		default:
			pos = append(pos, a)
		}
	}
	return len(pos) == 0 || list
}

// configRead: git config --get / --get-all / --get-regexp / --list (or the get / list subcommands, or one name alone),
// with no write option.
func configRead(args []string) bool {
	for _, a := range args {
		if oneOf(a, "--add", "--unset", "--unset-all", "--replace-all", "--rename-section", "--remove-section", "-e",
			"--edit") {
			return false
		}
	}
	for _, a := range args {
		if oneOf(a, "--get", "--get-all", "--get-regexp", "--get-urlmatch", "--get-color", "--get-colorbool", "--list",
			"-l") {
			return true
		}
	}
	pos := positionals(args, []string{"-f", "--file", "--blob", "--type", "--default", "--comment", "--value"})
	if len(pos) > 0 && oneOf(pos[0], "get", "list") {
		return true
	}
	return len(pos) == 1 && !oneOf(pos[0], "set", "unset", "rename-section", "remove-section", "edit")
}

// isMain: the git command run in dir (with --git-dir / --work-tree, or GIT_DIR / GIT_WORK_TREE) works on the main
// checkout.
func (g *guard) isMain(dir, gitDir, workTree string) bool {
	if gitDir != "" {
		p, ok := g.phys(gitDir, dir)
		return ok && p == g.repo.Common
	}
	if workTree != "" {
		p, ok := g.phys(workTree, dir)
		if !ok {
			return false
		}
		dir = p
	}
	if !project.IsDir(dir) {
		return false
	}
	r, err := project.Find(dir)
	return err == nil && r.Top == g.root
}

// checkPush: a push is allowed only for a non-worker whose role is in guards.allow_push (config.PushAllowed, T-786:
// an entry is a role, or a derived name read as its slot).
func (g *guard) checkPush(what string) {
	if g.cfg.MayPush(g.role) { // never a worker's or a driver's (T-751)
		return
	}
	deny(what + " is blocked — only a non-worker role listed in guards.allow_push may push; otherwise the user pushes")
}

// gh <args>: pushes are `repo create … --push`, `pr create` and `repo sync <repo>`.
func (g *guard) gh(W []string, k int) {
	n, i := len(W), k+1
	for i < n && strings.HasPrefix(W[i], "-") {
		i++
	}
	if i+1 >= n {
		return
	}
	g1, g2 := W[i], W[i+1]
	rest := W[i+2:]
	switch g1 + " " + g2 {
	case "pr create":
		g.checkPush("gh pr create (pushes the branch)")
	case "repo create":
		for _, a := range rest {
			if a == "--push" {
				g.checkPush("gh repo create --push")
			}
		}
	case "repo sync":
		operand := false
		for j := 0; j < len(rest); j++ {
			switch a := rest[j]; {
			case a == "-b" || a == "--branch" || a == "-s" || a == "--source":
				j++
			case strings.HasPrefix(a, "-"):
			default:
				operand = true
			}
		}
		if operand {
			g.checkPush("gh repo sync <repo>")
		}
	}
}

func oneOf(w string, set ...string) bool {
	for _, s := range set {
		if w == s {
			return true
		}
	}
	return false
}
