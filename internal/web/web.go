// Package web is `obor web` (T-667): a localhost page for every obor project on the machine. It serves the read model's
// JSON — each project's view as view.Build builds it for `obor status --json` (view.Load), and the drill-down pages of
// view.Detail — plus the page itself (T-760): a Svelte app whose source is web/ at the repo's root, built by `make web`
// into dist/ here, committed and embedded (the Go build needs no Node; TestDistFresh fails when dist/ is older than
// web/'s sources). Its own
// process, never inside serve: serve is the one writer with a replayed state machine, and a handler in its process
// would share its restarts. Localhost only, no auth: remote access is an SSH tunnel.
//
// Two verbs (T-753), the owner's setup edits from the Setup page: POST /api/config (a list of config changes, one
// commit, T-760) and POST
// /api/pause (set or clear the pause). Neither writes anything itself: each is a request to the project's serve (its
// app socket), the one writer; serve not running is 503. Every other route is GET or HEAD only (405). A POST is taken
// only on those two routes and only when its Origin is the page's own origin, its Host the listen address (no DNS
// rebinding) and its Content-Type application/json (403 otherwise): a page on another origin cannot send one.
package web

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"obor/internal/app/wire"
	"obor/internal/help"
	"obor/internal/project"
	"obor/internal/store"
	"obor/internal/view"
)

// DefaultListen is the address obor web listens on without --listen: localhost, a fixed port (said at start).
const DefaultListen = "127.0.0.1:8787"

// The server's bounds: one status request's wait, the long poll's longest hold, how often it asks the store meanwhile,
// and how many records one read of the event API returns.
const (
	askTimeout  = 5 * time.Second
	defaultWait = 20 * time.Second
	defaultPoll = 300 * time.Millisecond
	eventsLimit = 200
	defaultView = 20 // worded events in a project's view (obor status --json carries 20 too)
)

// Server is obor web's handlers over the machine's projects. The zero value is the real thing: project.Projects and
// view.Load; a test injects Projects and Build.
type Server struct {
	Projects func() []project.Project                            // the machine's projects (default: project.Projects)
	Build    func(context.Context, string) (view.Project, error) // one project's view by root (default: view.Load)
	Events   int                                                 // worded events in a view (0: 20)
	Wait     time.Duration                                       // how long /api/events holds a poll (0: 20s)
	Poll     time.Duration                                       // how often it asks the store (0: 300ms)
	// Listen is the address it serves on: a POST's Host must name it ("": DefaultListen).
	Listen string
	// Call is a request to the serve of the project at root (default: wire.Verdict on its socket; no socket:
	// errServeDown).
	Call func(ctx context.Context, root, method string, c wire.Call) (string, error)
	// Kit is the kit's root, whose README.md /api/help reads ("": project.KitRoot).
	Kit string
}

// errServeDown is a request the project's serve cannot take: it is not running (503).
var errServeDown = errors.New("obor serve is not running")

// callWait bounds a POST's request to serve: inside the socket's own 10 s per connection.
const callWait = 12 * time.Second

// Handler is the page and the API: /api/projects, /api/view, /api/setup, /api/task, /api/agent, /api/job,
// /api/events and /api/help, the two POST routes /api/config and /api/pause, and the page for every other path ("/" plus the hash
// routes the page itself reads).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/projects", s.projectsAPI)
	mux.HandleFunc("/api/view", s.viewAPI)
	mux.HandleFunc("/api/setup", s.setupAPI)
	mux.HandleFunc("/api/task", s.taskAPI)
	mux.HandleFunc("/api/agent", s.agentAPI)
	mux.HandleFunc("/api/job", s.jobAPI)
	mux.HandleFunc("/api/events", s.eventsAPI)
	mux.HandleFunc("/api/help", s.helpAPI)
	mux.HandleFunc("/api/config", s.configAPI)
	mux.HandleFunc("/api/pause", s.pauseAPI)
	mux.HandleFunc("/", s.page)
	return s.methods(mux)
}

// writeRoutes are the routes that take a POST (and only a POST).
var writeRoutes = map[string]bool{"/api/config": true, "/api/pause": true}

// methods is the request check in front of every route: GET and HEAD everywhere but the write routes; POST only on
// those, and only from the page itself (sameOrigin; 403); anything else 405.
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
			w.Header().Set("Allow", "GET, HEAD")
			fail(w, http.StatusMethodNotAllowed, "obor web takes a POST on /api/config and /api/pause only")
			return
		}
		h.ServeHTTP(w, r)
	})
}

// sameOrigin is why a POST is not the page's own ("": it is): its Host is not the listen address (a DNS rebinding
// name; localhost and 127.0.0.1 are one host), its Origin is not http://<Host>, or its body is not
// application/json (a form post another page could send).
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

// listenHost: host (a request's Host, with its port) is the address obor web listens on, or that address with
// localhost for a loopback IP and the other way round.
func (s *Server) listenHost(host string) bool {
	listen := s.Listen
	if listen == "" {
		listen = DefaultListen
	}
	if host == listen {
		return true
	}
	lh, lp, err := net.SplitHostPort(listen)
	h, p, err2 := net.SplitHostPort(host)
	if err != nil || err2 != nil || p != lp {
		return false
	}
	loop := func(x string) bool {
		ip := net.ParseIP(x)
		return x == "localhost" || ip != nil && ip.IsLoopback()
	}
	return loop(lh) && loop(h) && (h == "localhost" || lh == "localhost")
}

// ProjectsEntry is one row of /api/projects: the project's name and root, whether serve is up, its busy and free lanes,
// its needs-you count, its queue length (the merge loop's tasks and the dispatches waiting), the steps running without
// an agent, the role agents at work (T-713) and why its view could not be built.
type ProjectsEntry struct {
	Name     string         `json:"name"`
	Root     string         `json:"root"`
	Up       bool           `json:"up"`
	Busy     int            `json:"busy"`
	Free     int            `json:"free"`
	NeedsYou int            `json:"needs_you"`
	Queue    int            `json:"queue"`
	Running  []view.Running `json:"running"`
	Roles    []view.Role    `json:"roles"`
	// Background is how many long-running background processes the project's agents have (T-714).
	Background int `json:"background"`
	// Lanes is each lane's state column and task (the fleet page's lane strip, T-760), FullCheck the last full check,
	// Paused the pause in force and Events the project's newest worded events (at most fleetEvents).
	Lanes     []LaneDot       `json:"lanes"`
	FullCheck *view.FullCheck `json:"full_check"`
	Paused    *view.Pause     `json:"paused"`
	Events    []view.Event    `json:"events"`
	Error     string          `json:"error,omitempty"`
}

// LaneDot is one lane as the fleet page shows it: its name, its state column, its configured type, whether it is free
// and its task (view.Task, T-883: a free lane's is its last branch's id alone); a busy lane's task is the view's, with
// what the fleet's running list shows (its title, serve's state and step of it, the agent), and since when the column
// holds (epoch seconds). The task's lane is the dot's own.
type LaneDot struct {
	Lane   string `json:"lane"`
	Column string `json:"column"`
	Type   string `json:"type,omitempty"`
	Free   bool   `json:"free"`
	Since  *int64 `json:"since,omitempty"`
	view.Task
}

// fleetEvents is how many of each project's newest events /api/projects carries.
const fleetEvents = 5

// projectsAPI is the machine's projects with their counts.
func (s *Server) projectsAPI(w http.ResponseWriter, r *http.Request) {
	out := make([]ProjectsEntry, 0)
	for _, p := range s.projects() {
		e := ProjectsEntry{Name: p.Name, Root: p.Root, Running: []view.Running{}, Roles: []view.Role{},
			Lanes: []LaneDot{}, Events: []view.Event{}}
		ctx, cancel := context.WithTimeout(r.Context(), askTimeout)
		v, err := s.build(ctx, p.Root)
		cancel()
		if err != nil {
			e.Error = err.Error()
		} else {
			e.Up, e.NeedsYou, e.Queue = v.Serve.Up, len(v.NeedsYou), len(v.Queue.Merge)+len(v.Queue.Waiting)
			e.Running = append(e.Running, v.Running...)
			e.Background = v.BackgroundCount()
			e.FullCheck, e.Paused = v.FullCheck, v.Paused
			e.Events = append(e.Events, v.Events[:min(fleetEvents, len(v.Events))]...)
			for _, r := range v.Roles {
				if r.Status == "working" || r.Brief != nil { // a brief waiting to be delivered counts, as on the project page
					e.Roles = append(e.Roles, r)
				}
			}
			for _, l := range v.Lanes {
				d := LaneDot{Lane: l.Lane, Column: l.Column, Type: l.Type, Free: l.Free}
				if l.Task != nil {
					d.Task.Task = *l.Task
				}
				if l.Free { // a free lane keeps its last branch's task
					e.Free++
				} else {
					if l.Task != nil {
						d.Task = v.Tasks[*l.Task]
					}
					d.Agent, d.Since = l.Agent, l.Since
					e.Busy++
				}
				e.Lanes = append(e.Lanes, d)
			}
		}
		out = append(out, e)
	}
	writeJSON(w, http.StatusOK, out)
}

// viewAPI is /api/view?project=<name>: the project's view, the struct `obor status --json` prints.
func (s *Server) viewAPI(w http.ResponseWriter, r *http.Request) {
	p, ok := s.project(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), askTimeout)
	defer cancel()
	v, err := s.build(ctx, p.Root)
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// setupAPI is /api/setup?project=<name>: the project's setup read model (T-750), built from the view it already asks
// serve for and the project's config and store.
func (s *Server) setupAPI(w http.ResponseWriter, r *http.Request) {
	p, ok := s.project(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), askTimeout)
	defer cancel()
	v, err := s.build(ctx, p.Root)
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	setup, err := view.Detail{Root: p.Root, Project: v}.Setup()
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, setup)
}

// taskAPI is /api/task?project=<name>&task=<id>: the task's page.
func (s *Server) taskAPI(w http.ResponseWriter, r *http.Request) {
	d, id, ok := s.detail(w, r, "task")
	if !ok {
		return
	}
	t, ok := d.Task(id)
	if !ok {
		fail(w, http.StatusNotFound, "no such task: "+id)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// agentAPI is /api/agent?project=<name>&agent=<name>: the agent's page.
func (s *Server) agentAPI(w http.ResponseWriter, r *http.Request) {
	d, name, ok := s.detail(w, r, "agent")
	if !ok {
		return
	}
	a, ok := d.Agent(name)
	if !ok {
		fail(w, http.StatusNotFound, "no such agent: "+name)
		return
	}
	writeJSON(w, http.StatusOK, a)
}

// jobAPI is /api/job?project=<name>&job=<id>: the job's prompt text (the project's root is all the lookup needs: no
// view).
func (s *Server) jobAPI(w http.ResponseWriter, r *http.Request) {
	p, ok := s.project(w, r)
	if !ok {
		return
	}
	id := r.URL.Query().Get("job")
	if id == "" {
		fail(w, http.StatusBadRequest, "missing job")
		return
	}
	prompt, ok := view.Detail{Root: p.Root}.Job(id)
	if !ok {
		fail(w, http.StatusNotFound, "no such job: "+id)
		return
	}
	writeJSON(w, http.StatusOK, prompt)
}

// Help is /api/help (T-854): every obor command (help.Commands) by group, and README's "Install" and "Set up a project"
// sections.
type Help struct {
	Groups   []string       `json:"groups"`
	Commands []help.Command `json:"commands"`
	Sections []help.Section `json:"sections"`
}

// helpAPI is /api/help: the command table and the kit's README sections, read at each request (no copy in the binary).
// An unreadable README or one without those sections is 500, naming why.
func (s *Server) helpAPI(w http.ResponseWriter, r *http.Request) {
	kit := s.Kit
	if kit == "" {
		kit = project.KitRoot(project.OSEnv)
	}
	if kit == "" {
		fail(w, http.StatusInternalServerError, "no kit root: obor web cannot find README.md")
		return
	}
	readme, err := os.ReadFile(filepath.Join(kit, "README.md"))
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	sections, err := help.Sections(string(readme))
	if err != nil {
		fail(w, http.StatusInternalServerError, filepath.Join(kit, "README.md")+": "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, Help{Groups: help.Groups, Commands: help.Commands, Sections: sections})
}

// configBody is POST /api/config's body (T-760): the changes, each a key path (config.EditKeys) and its new value, and
// the config.json revision the page read them on (the setup's config_rev).
type configBody struct {
	Changes []wire.ConfigChange `json:"changes"`
	Base    string              `json:"base"`
}

// maxChanges bounds one POST /api/config: a setup page has far fewer fields.
const maxChanges = 100

// pauseBody is POST /api/pause's body: a pause's reason and its duration (a Go duration, "": until cleared), or clear;
// with model, that model's pause (obor pause --model, T-766).
type pauseBody struct {
	Reason string `json:"reason"`
	For    string `json:"for"`
	Clear  bool   `json:"clear"`
	Model  string `json:"model"`
}

// replyBody is a POST's answer: serve's reply line.
type replyBody struct {
	Reply string `json:"reply"`
}

// configAPI is POST /api/config?project=<name>: the setup changes, one config_set request to the project's serve (it
// validates them together, writes and commits them as one, or refuses them all: a 400 with its reason). The base is
// required: serve refuses the changes when config.json is not the one the page read.
func (s *Server) configAPI(w http.ResponseWriter, r *http.Request) {
	p, ok := s.project(w, r)
	if !ok {
		return
	}
	var b configBody
	if !readBody(w, r, &b) {
		return
	}
	if len(b.Changes) == 0 || len(b.Changes) > maxChanges || b.Base == "" {
		fail(w, http.StatusBadRequest, fmt.Sprintf("changes (1 to %d) and the base the page read are required", maxChanges))
		return
	}
	for _, c := range b.Changes {
		if c.Key == "" || len(c.Value) == 0 {
			fail(w, http.StatusBadRequest, "every change needs a key and a value")
			return
		}
	}
	s.ask(w, r, p, wire.MethodConfig, wire.Call{Changes: b.Changes, Base: b.Base})
}

// pauseAPI is POST /api/pause?project=<name>: set the project's pause (a reason, an optional duration) or clear it,
// the pause request obor pause sends.
func (s *Server) pauseAPI(w http.ResponseWriter, r *http.Request) {
	p, ok := s.project(w, r)
	if !ok {
		return
	}
	var b pauseBody
	if !readBody(w, r, &b) {
		return
	}
	c := wire.Call{Clear: b.Clear, Model: strings.TrimSpace(b.Model)}
	if !b.Clear {
		c.Note = strings.TrimSpace(b.Reason)
		if c.Note == "" {
			fail(w, http.StatusBadRequest, "a pause needs a reason")
			return
		}
		if b.For != "" {
			d, err := time.ParseDuration(b.For)
			if err != nil || d <= 0 {
				fail(w, http.StatusBadRequest, "for: "+strconv.Quote(b.For)+" is no duration (90m, 3h)")
				return
			}
			c.Until = time.Now().Add(d).Unix()
		}
	}
	s.ask(w, r, p, wire.MethodPause, c)
}

// readBody decodes the request's JSON body into v (at most 64 KiB, unknown fields refused): false when the 400 is
// written.
func readBody(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		fail(w, http.StatusBadRequest, "unreadable body: "+err.Error())
		return false
	}
	return true
}

// ask sends c (the owner's, from the web: no agent, no role) to p's serve and writes its reply: 200 with the reply,
// 503 when serve is not running, 400 with serve's refusal.
func (s *Server) ask(w http.ResponseWriter, r *http.Request, p project.Project, method string, c wire.Call) {
	c.Root, c.CallID, c.Source = p.Root, wire.NewCallID(), "web"
	ctx, cancel := context.WithTimeout(r.Context(), callWait)
	defer cancel()
	call := s.Call
	if call == nil {
		call = serveCall
	}
	reply, err := call(ctx, p.Root, method, c)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, replyBody{Reply: reply})
	case errors.Is(err, errServeDown) || errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ENOENT) ||
		errors.Is(err, context.DeadlineExceeded):
		fail(w, http.StatusServiceUnavailable, fmt.Sprintf("%s: %v", p.Name, err))
	default:
		fail(w, http.StatusBadRequest, err.Error())
	}
}

// serveCall is Server.Call's default: wire.Verdict on the project's socket; no socket is errServeDown at once (no
// retries for a serve that is not there).
func serveCall(ctx context.Context, root, method string, c wire.Call) (string, error) {
	sock := wire.SocketPath(root)
	if _, err := os.Stat(sock); err != nil {
		return "", fmt.Errorf("%w (no %s): obor start starts it", errServeDown, sock)
	}
	return wire.Verdict(ctx, sock, method, c)
}

// detail is the request's project's Detail and the value of param: ok false when the response is already written (an
// unknown project, a missing param or a view that cannot be built).
func (s *Server) detail(w http.ResponseWriter, r *http.Request, param string) (view.Detail, string, bool) {
	p, ok := s.project(w, r)
	if !ok {
		return view.Detail{}, "", false
	}
	id := r.URL.Query().Get(param)
	if id == "" {
		fail(w, http.StatusBadRequest, "missing "+param)
		return view.Detail{}, "", false
	}
	ctx, cancel := context.WithTimeout(r.Context(), askTimeout)
	defer cancel()
	v, err := s.build(ctx, p.Root)
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return view.Detail{}, "", false
	}
	return view.Detail{Root: p.Root, Project: v}, id, true
}

// eventsAPI is /api/events?project=<name>&since=<seq>: a long poll on the store's seq. It answers at once when a record
// newer than since is there, and otherwise holds the request until one is (or its wait ends): the page keeps one poll
// open and shows a new record as it lands.
func (s *Server) eventsAPI(w http.ResponseWriter, r *http.Request) {
	p, ok := s.project(w, r)
	if !ok {
		return
	}
	since := int64(0)
	if q := r.URL.Query().Get("since"); q != "" {
		n, err := strconv.ParseInt(q, 10, 64)
		if err != nil || n < 0 {
			fail(w, http.StatusBadRequest, "since is no seq: "+q)
			return
		}
		since = n
	}
	db, _ := store.OpenAt(p.Root)
	if db == nil {
		writeJSON(w, http.StatusOK, view.EventsPage{Seq: since, Events: []view.Event{}})
		return
	}
	defer db.Close()
	deadline := time.Now().Add(s.wait())
	for {
		page, err := view.EventsSince(db, since, eventsLimit)
		if err != nil {
			fail(w, http.StatusInternalServerError, err.Error())
			return
		}
		if page.Seq > since || !time.Now().Before(deadline) {
			writeJSON(w, http.StatusOK, page)
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(s.poll()):
		}
	}
}

// dist is the built page (web/, `make web`): index.html, its hashed assets and source.sha256.
//
//go:embed all:dist
var dist embed.FS

// distFS is dist/ as the page's root.
var distFS, _ = fs.Sub(dist, "dist")

// assets serves the built page's hashed files: a name never changes its content, so they are cached for good.
var assets = http.FileServerFS(distFS)

// page is the built page: its hashed files under /assets/, and index.html for every other path (the page routes by
// the URL's hash).
func (s *Server) page(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		fail(w, http.StatusNotFound, "no such route: "+r.URL.Path)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/assets/") {
		if _, err := fs.Stat(distFS, strings.TrimPrefix(r.URL.Path, "/")); err != nil {
			fail(w, http.StatusNotFound, "no such file: "+r.URL.Path)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		assets.ServeHTTP(w, r)
		return
	}
	index, err := fs.ReadFile(distFS, "index.html")
	if err != nil {
		fail(w, http.StatusInternalServerError, "the page is not built: make web")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(index)
}

// project is the project the request names: ok false when it names none or an unknown one (the response is written).
func (s *Server) project(w http.ResponseWriter, r *http.Request) (project.Project, bool) {
	name := r.URL.Query().Get("project")
	if name == "" {
		fail(w, http.StatusBadRequest, "missing project")
		return project.Project{}, false
	}
	for _, p := range s.projects() {
		if p.Name == name {
			return p, true
		}
	}
	fail(w, http.StatusNotFound, "no such project: "+name)
	return project.Project{}, false
}

// projects is the machine's projects.
func (s *Server) projects() []project.Project {
	if s.Projects != nil {
		return s.Projects()
	}
	return project.Projects(project.OSEnv)
}

// build is one project's view, as obor status --json builds it.
func (s *Server) build(ctx context.Context, root string) (view.Project, error) {
	if s.Build != nil {
		return s.Build(ctx, root)
	}
	n := s.Events
	if n <= 0 {
		n = defaultView
	}
	return view.Load(ctx, root, time.Now(), n)
}

// wait is how long /api/events holds a poll before it answers empty.
func (s *Server) wait() time.Duration {
	if s.Wait > 0 {
		return s.Wait
	}
	return defaultWait
}

// poll is how often /api/events asks the store while it holds a poll.
func (s *Server) poll() time.Duration {
	if s.Poll > 0 {
		return s.Poll
	}
	return defaultPoll
}

// ListenAddr is a --listen value as an address to serve on: "" is DefaultListen; a host that is not loopback — and no
// host at all (":8787", every interface) — is refused, obor web is localhost only.
func ListenAddr(listen string) (string, error) {
	if listen == "" {
		listen = DefaultListen
	}
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "", fmt.Errorf("--listen %q: %v (want 127.0.0.1:<port>)", listen, err)
	}
	if host == "" {
		return "", fmt.Errorf("--listen %q: no host: obor web is localhost only (127.0.0.1:%s)", listen, port)
	}
	if host != "localhost" {
		if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
			return "", fmt.Errorf("--listen %q: %s is not loopback: obor web is localhost only", listen, host)
		}
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 0 || n > 65535 {
		return "", fmt.Errorf("--listen %q: %q is no port", listen, port)
	}
	return net.JoinHostPort(host, port), nil
}

// writeJSON writes v as the response body.
func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

// fail writes one error as the response body.
func fail(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, struct {
		Error string `json:"error"`
	}{msg})
}
