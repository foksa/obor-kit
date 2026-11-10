# Plan 002: obor web answers only to the host names it was given: loopback, plus `--allow-host`

> **Executor instructions**: Follow this plan step by step. Run every verification command and confirm the expected
> result before moving to the next step. If anything in the "STOP conditions" section occurs, stop and report; do not
> improvise. When done, update the status row for this plan in `docs/research/improve-plans.md`, unless the lead
> dispatched you and keeps the index.
>
> **Drift check (run first)**: `git diff --stat a73e814c..HEAD -- internal/web cmd/obor/web.go cmd/obor/web_test.go cmd/obor/follow_test.go internal/help/help.go skills/lead/commands.md`
> If any of these changed since this plan was written, compare the "Current state" excerpts against the live code
> before proceeding; on a mismatch, treat it as a STOP condition.

## Status

- **Priority**: P1
- **Effort**: S
- **Risk**: LOW
- **Depends on**: none
- **Category**: security
- **Planned at**: commit `a73e814c`, 2026-11-10

Spec lines, if the lead turns this into an obor spec: `Difficulty: medium` · `Guarantee: refusal` ·
`Replaces: Server.Listen and listenHost (the POST-only Host check), deleted here` ·
`Adds: obor web's --allow-host flag; web.Server.Hosts; a 403 on any route for a Host that is not one of its names`.

## Why this matters

`obor web` is a localhost page with no login that shows every obor project on the machine: specs, reports, job
prompts (which carry diffs) and events. Its two write routes already refuse a request whose `Host` header is not the
listen address, which stops DNS rebinding (a web page on another site whose domain is re-pointed at `127.0.0.1`).
Its read routes have no such check, so the same attack can read everything. This plan puts one Host check in front
of every route. The owner plans to reach obor web from other devices, so the check is written as "the names this
server answers to": loopback names on any port (an SSH tunnel on any local port keeps working), plus names the owner
adds with a new `--allow-host` flag (a Tailscale or reverse-proxy name). Later multi-device work adds names in that
one place.

## Current state

Files:

- `internal/web/web.go`: the HTTP server. `methods` (lines 105-128) is the check in front of every route;
  `sameOrigin` (133-144) and `listenHost` (148-166) are the POST-only check; `Server.Listen` (line 69) feeds it.
- `cmd/obor/web.go`: the `obor web` command: flag parsing, the listener, and the re-exec onto a new binary.
- `internal/help/help.go:56`: the command's usage line.
- `skills/lead/commands.md:15-17`: the lead skill's paragraph on `obor web`.
- Tests: `internal/web/web_test.go`, `internal/web/post_test.go`, `cmd/obor/web_test.go`, `cmd/obor/follow_test.go`.

`internal/web/web.go:107-128` today (reads pass with no Host check):

```go
func (s *Server) methods(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		write := writeRoutes[r.URL.Path]
		switch {
		case write && r.Method == http.MethodPost:
			if why := s.sameOrigin(r); why != "" {
				fail(w, http.StatusForbidden, why)
				return
			}
		case !write && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		case write:
			w.Header().Set("Allow", "POST")
			fail(w, http.StatusMethodNotAllowed, r.URL.Path+" takes a POST only")
			return
		default:
			...
```

`internal/web/web.go:133-144` today:

```go
func (s *Server) sameOrigin(r *http.Request) string {
	if !s.listenHost(r.Host) {
		return "Host " + strconv.Quote(r.Host) + " is not the address obor web listens on"
	}
	if o := r.Header.Get("Origin"); o != "http://"+r.Host {
		return "Origin " + strconv.Quote(o) + " is not this page's (http://" + r.Host + ")"
	}
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		return "Content-Type " + strconv.Quote(r.Header.Get("Content-Type")) + " is not application/json"
	}
	return ""
}
```

`listenHost` (148-166) accepts a Host only when its **port equals the listen port** and both hosts are loopback, so a
POST through an SSH tunnel on another local port is refused today.

`cmd/obor/web.go:56` builds the server, and line 80 re-execs it:

```go
	srv := &http.Server{Handler: (&web.Server{Events: statusEvents, Listen: ln.Addr().String()}).Handler(), ReadHeaderTimeout: 5 * time.Second}
	...
	if err := selfExec(project.KitRoot(project.OSEnv), []string{"web", "--listen", ln.Addr().String()}); err != nil {
```

Flag parsing there is a hand-written loop (lines 29-44) with `--listen <v>` and `--listen=<v>`; an unknown argument
prints `help.Usage("web")` and returns 2.

`internal/help/help.go:56`:

```go
	{"web", "obor web [--listen 127.0.0.1:<port>]",
		"a localhost page for every project on this machine: lanes, queue, needs-you, events, reports, setup, help", "project"},
```

Facts about the tests that matter:

- `internal/web/web_test.go:160-165`, the `get` helper, builds its request with `httptest.NewRequest`, whose `Host`
  is `example.com`. Once every route checks the Host, every GET test gets a 403 unless the helper sets a loopback
  Host. The same holds for the two direct `httptest.NewRequest` calls at `web_test.go:530` and `post_test.go:151`.
- `internal/web/post_test.go:83` sets `s.Listen`; the field goes away.
- `post_test.go:110-111` has two refused cases: `{"rebinding Host", "evil.example:8787", nil}` (stays refused) and
  `{"another Host port", "127.0.0.1:9999", nil}` (becomes allowed: that request is what a tunnel on another port sends).
- `web_test.go:444` uses `httptest.NewServer`, which listens on `127.0.0.1:<random port>`; its requests carry that
  as Host and pass the new check unchanged.
- `cmd/obor/web_test.go:28` asserts stderr contains `usage: obor web [--listen 127.0.0.1:<port>]`; keep that text as
  the start of the new usage line so the assert holds.
- `cmd/obor/follow_test.go:99` asserts the re-exec's argv has exactly 4 elements (`<shim> web --listen <addr>`); with
  no `--allow-host` given it must stay 4.

Why dropping the port comparison is safe: the browser sets `Host` to the URL it is really calling, and a page cannot
forge it. A POST is still taken only when `Origin` equals `scheme://<Host>` exactly, so a page served from another
local port (a different origin) is still refused. The Host name check alone is what stops rebinding.

Repo conventions that apply: plain functions, early returns, comments in full sentences that say what a thing is and
why (see any function comment in `internal/web/web.go`); "an empty derived value is an error, never a default"
(`AGENTS.md`); a decision or a refusal is a unit test on the handler (`httptest.NewRecorder`), never a real server.

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Build | `go build ./...` | exit 0, no output |
| Vet the two packages | `go vet -tags integration ./internal/web/ ./cmd/obor/` | exit 0 |
| Unit tests, web | `go test ./internal/web/` | `ok  	obor/internal/web` |
| All tests, web | `go test -tags integration ./internal/web/` | `ok` |
| All tests, cmd | `go test -tags integration ./cmd/obor/` | `ok` (about 25 s) |
| Skill text readers | `go test -tags integration -run 'Skill' ./cmd/obor/` | `ok` |
| Dead code | `make -s deadcode` | exit 0, no output |

Run one test command at a time (the guard denies two at once, `-count` above 1 and `-race`).

## Scope

**In scope** (the only files you should modify):
- `internal/web/web.go`
- `internal/web/web_test.go`, `internal/web/post_test.go`
- `cmd/obor/web.go`, `cmd/obor/web_test.go`, `cmd/obor/follow_test.go`
- `internal/help/help.go` (the one usage line)
- `skills/lead/commands.md` (the `## obor web` paragraph)

**Out of scope** (do NOT touch):
- `web.ListenAddr` and the rule that `--listen` takes a loopback address only. Listening on another interface needs
  a login first; that is the multi-device design, not this plan.
- Any login, token or TLS.
- `web/` (the Svelte sources) and `internal/web/dist/`: the page calls relative URLs (`web/src/lib/api.ts`), so it
  works under any host name unchanged.
- `docs/design.md`: protected, the lead's. The sentence to update is under Maintenance notes.

## Git workflow

- If you are an obor worker (your job came through `job_next`): work in your lane, make no commits, finish with
  `task_done`.
- Otherwise: work in a separate git worktree on a branch `advisor/002-web-host-allowlist` cut from `staging`, never
  in the main checkout (the project's live `obor serve` runs `dist/obor` from there). Commit message style: one
  sentence saying what is true now. Do not push.

## Steps

### Step 1: One Host check in front of every route (`internal/web/web.go`)

1. In `Server`, replace the `Listen string` field and its comment with:

   ```go
   	// Hosts are the extra names obor web answers to (--allow-host: a tunnel's or proxy's name), lower case, no port.
   	// A loopback name on any port is always one of its names.
   	Hosts []string
   ```

2. Add three small functions, replacing `listenHost` (delete it):

   ```go
   // hostName is a Host header's name without its port, lower case ("[::1]:8787" is "::1").
   func hostName(host string) string {
   	if h, _, err := net.SplitHostPort(host); err == nil {
   		host = h
   	}
   	return strings.ToLower(strings.Trim(host, "[]"))
   }

   // loopback: name is localhost or a loopback IP.
   func loopback(name string) bool {
   	ip := net.ParseIP(name)
   	return name == "localhost" || ip != nil && ip.IsLoopback()
   }

   // answersTo: host (a request's Host) is one of obor web's names: a loopback name on any port (the page itself, an
   // SSH tunnel on any local port) or one of Hosts. Any other Host is a DNS rebinding name, or a proxy the owner did
   // not name.
   func (s *Server) answersTo(host string) bool {
   	name := hostName(host)
   	return name != "" && (loopback(name) || slices.Contains(s.Hosts, name))
   }
   ```

3. In `methods`, before the `switch`, refuse a foreign Host on every route and method:

   ```go
   		if !s.answersTo(r.Host) {
   			fail(w, http.StatusForbidden, "Host "+strconv.Quote(r.Host)+" is not a name obor web answers to (loopback, or obor web --allow-host <name>)")
   			return
   		}
   ```

4. In `sameOrigin`, delete the `listenHost` block and replace the Origin check: the Origin must be `http://<Host>`,
   or `https://<Host>` when the Host's name is one of `Hosts` (a TLS-terminating proxy); a loopback Host takes
   `http://` only, as today.

   ```go
   	o := r.Header.Get("Origin")
   	if o != "http://"+r.Host && !(o == "https://"+r.Host && slices.Contains(s.Hosts, hostName(r.Host))) {
   		return "Origin " + strconv.Quote(o) + " is not this page's (http://" + r.Host + ")"
   	}
   ```

5. Add the flag value's check:

   ```go
   // AllowHost is an --allow-host value as one of obor web's names: a DNS name or an IPv4 address, lower case, with
   // no scheme, port, path or space ("" and anything else is refused, naming why).
   func AllowHost(v string) (string, error) {
   	name := strings.ToLower(strings.TrimSpace(v))
   	if name == "" || strings.ContainsAny(name, "/: \t") {
   		return "", fmt.Errorf("--allow-host %q: want a host name alone (mac.example.net), no scheme, port or path", v)
   	}
   	return name, nil
   }
   ```

6. Update the package comment (lines 13-15) and the comments of `methods` and `sameOrigin` to say the new rule: every
   request's Host must be one of obor web's names (403); a POST also needs the page's own Origin and a JSON body.
   Add `"slices"` to the imports.

### Step 2: The flag, and the re-exec keeps it (`cmd/obor/web.go`, `internal/help/help.go`)

1. In `webCmd`'s loop add `--allow-host <name>` and `--allow-host=<name>` (repeatable), each value through
   `web.AllowHost`; on its error: `errf("obor web: %v", err)` and `return 2`. Collect them in `hosts []string`.
2. Build the server with `&web.Server{Events: statusEvents, Hosts: hosts}` (the `Listen:` field is gone).
3. When `hosts` is not empty, print one more line after the "obor web: http://…" line:
   `obor web: also answers as <name>[, <name>…] (no login: whoever reaches that name reads every project)`.
4. The re-exec passes each on: build the argv as `[]string{"web", "--listen", addr}` and append
   `"--allow-host", h` for every host, then call `selfExec` with it.
5. Update `webCmd`'s comment, and `internal/help/help.go:56`'s usage to
   `obor web [--listen 127.0.0.1:<port>] [--allow-host <name>]…`.

**Verify (steps 1 and 2 together)**: `go build ./...` → exit 0; `go vet -tags integration ./internal/web/ ./cmd/obor/`
fails only in `_test.go` files that still name `Listen` (fixed in step 3).

### Step 3: Tests in `internal/web`

- `web_test.go` `get` helper: build the request, set `r.Host = DefaultListen`, then serve it.
- `web_test.go:530` and `post_test.go:151` (the direct `httptest.NewRequest` calls): set `Host` to `DefaultListen`
  the same way, so they still test the method rule (405).
- `post_test.go` `TestPostOnlyFromThePage`: drop the `s.Listen` assignment; move `"another Host port"` out of the
  refused table into an assert of its own that it is taken (200): "a tunnel on another local port". `"rebinding Host"`
  and `"https origin"` stay refused.
- New `TestEveryRouteRefusesAForeignHost` (in `web_test.go`): for each of `/api/projects`, `/api/view?project=pj`,
  `/api/setup?project=pj`, `/api/task?project=pj&task=T-01`, `/api/job?project=pj&job=T-01/1/work`,
  `/api/events?project=pj`, `/api/help` and `/`: a GET with Host `evil.example:8787` is 403 and its body names the
  Host; with Host `localhost:9000` it is not 403. Use `fixture(t)` and `httptest.NewRecorder`.
- New `TestAllowHost` (in `post_test.go`): with `s.Hosts = []string{"mac.example.net"}` a GET with Host
  `mac.example.net` is 200; a POST to `/api/pause?project=pj` with Origin `https://mac.example.net` is 200 and with
  Origin `http://mac.example.net` is 200; a GET with Host `other.example.net` is 403; with `s.Hosts` empty the first
  GET is 403. Also the value check: `AllowHost` returns `mac.example.net` for `" Mac.Example.NET "` and an error for
  `""`, `"https://x"`, `"x:8787"` and `"x/y"`.

**Verify**: `go test ./internal/web/` → `ok`; then `go test -tags integration ./internal/web/` → `ok`.

### Step 4: Tests in `cmd/obor`, and the skill text

- `cmd/obor/web_test.go`: add to `TestWebRefusesNonLocalhostListen` (or a new test beside it, same pattern) that
  `obor web --allow-host https://x` and `obor web --allow-host` (no value) exit 2 with `obor web: ` or the usage on
  stderr and nothing on stdout.
- `cmd/obor/follow_test.go` `TestWebFollowsNewBinaryOnItsPort`: add a case started with
  `webCmd([]string{"--listen", <addr>, "--allow-host", "mac.example.net"})` and assert the exec'd argv ends
  `--allow-host mac.example.net` (6 elements); the existing cases keep their 4.
- `skills/lead/commands.md`, the `## obor web` paragraph: after the sentence about `--listen`, add one sentence:
  `` Every request's Host must be a loopback name (any port: an SSH tunnel works) or one named with `--allow-host
  <name>` (a proxy's or tailnet's name; no login, so name one only on a network you trust). ``

**Verify**: `go test -tags integration ./cmd/obor/` → `ok`; `make -s deadcode` → exit 0 with no output (it fails if
`listenHost` or another function is left unused).

## Test plan

- Added: `TestEveryRouteRefusesAForeignHost`, `TestAllowHost`, one `--allow-host` refusal case and one re-exec case.
  All are handler or function-call tests except the two in `cmd/obor`, which extend existing tests of the same
  entry points.
- Changed: the `get` helper and two direct requests set a loopback Host; `"another Host port"` flips from refused to
  taken, with the reason in its comment.
- Deleted: nothing. `TestListenLocalhostOnly` and `TestWebRefusesNonLocalhostListen`'s `--listen` cases stay as they
  are.

## Done criteria

- [ ] `grep -n 'listenHost\|s\.Listen\b\|Listen:' internal/web/*.go cmd/obor/web.go` prints nothing
- [ ] `grep -c 'answersTo' internal/web/web.go` prints at least `2`
- [ ] `go build ./...` exits 0
- [ ] `go test -tags integration ./internal/web/` passes, including the two new tests
- [ ] `go test -tags integration ./cmd/obor/` passes
- [ ] `make -s deadcode` exits 0 with no output
- [ ] `gofmt -l cmd internal` prints nothing
- [ ] `git status --short` lists only in-scope files (and the index, if you keep it)

## STOP conditions

Stop and report back (do not improvise) if:

- A test outside the files in scope fails because its request carries a non-loopback Host (another package building
  requests against `web.Server`): list it; do not edit it.
- `go test -tags integration ./cmd/obor/` fails in a test that reads the help table or the skill texts in a way this
  plan did not foresee (for example a golden file of `obor help`), twice after one fix attempt.
- You find a caller that reaches obor web under a non-loopback name today (a script or doc in the repo): the default
  list would break it; report it.
- The fix seems to need a change to `ListenAddr` or to the page's sources.

## Maintenance notes

- For the lead: `docs/design.md`, section "obor web's write path" (around line 282), says "the listen address as
  Host (no DNS rebinding)" for a POST only. After this plan the rule is: every request's Host is a loopback name or
  an `--allow-host` name (403 otherwise); a POST also needs its own Origin and a JSON body.
- Multi-device later: a non-loopback `--listen` and a login are the missing parts. The Host rule then stays; its
  list of names grows through `Server.Hosts`. Do not add a second check. This plan is the first step of the viewer
  path in `docs/research/improve-architecture.md`, "One serve, several devices".
- `--allow-host` gives no protection by itself: it only says which name requests may arrive under. Reads and the two
  writes are open to whoever can reach that name, so it belongs behind a tunnel or a private network.
- Reviewer: check that no route is registered outside `methods` (a route added with its own `http.Handle` would skip
  the Host check), and that `obor web` started under launchd or a script with `--allow-host` keeps the flag across
  its self re-exec.
