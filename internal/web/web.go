// Package web is `obor web` (T-667): a read-only localhost page for every obor project on the machine, standard
// library only. It serves the read model's JSON — each project's view as view.Build builds it for `obor status --json`
// (view.Load), and the drill-down pages of view.Detail — plus one embedded HTML page (vanilla JS, no framework, no
// build step). Its own process, never inside serve: serve is the one writer with a replayed state machine, and a
// handler in its process would share its restarts. Localhost only, no auth: remote access is an SSH tunnel. No verbs:
// every route is a GET.
package web

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

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
}

// Handler is the page and the API: /api/projects, /api/view, /api/task, /api/agent, /api/job and /api/events, and the
// page for every other path ("/" plus the hash routes the page itself reads).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/projects", s.projectsAPI)
	mux.HandleFunc("/api/view", s.viewAPI)
	mux.HandleFunc("/api/task", s.taskAPI)
	mux.HandleFunc("/api/agent", s.agentAPI)
	mux.HandleFunc("/api/job", s.jobAPI)
	mux.HandleFunc("/api/events", s.eventsAPI)
	mux.HandleFunc("/", s.page)
	return readOnly(mux)
}

// readOnly refuses every method but GET and HEAD: obor web has no verbs.
func readOnly(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			fail(w, http.StatusMethodNotAllowed, "obor web is read-only")
			return
		}
		h.ServeHTTP(w, r)
	})
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
	Background int    `json:"background"`
	Error      string `json:"error,omitempty"`
}

// projectsAPI is the machine's projects with their counts.
func (s *Server) projectsAPI(w http.ResponseWriter, r *http.Request) {
	out := make([]ProjectsEntry, 0)
	for _, p := range s.projects() {
		e := ProjectsEntry{Name: p.Name, Root: p.Root, Running: []view.Running{}, Roles: []view.Role{}}
		ctx, cancel := context.WithTimeout(r.Context(), askTimeout)
		v, err := s.build(ctx, p.Root)
		cancel()
		if err != nil {
			e.Error = err.Error()
		} else {
			e.Up, e.NeedsYou, e.Queue = v.Serve.Up, len(v.NeedsYou), len(v.Queue.Merge)+len(v.Queue.Waiting)
			e.Running = append(e.Running, v.Running...)
			e.Background = v.BackgroundCount()
			for _, r := range v.Roles {
				if r.Status == "working" || r.Brief != nil { // a brief waiting to be delivered counts, as on the project page
					e.Roles = append(e.Roles, r)
				}
			}
			for _, l := range v.Lanes {
				if l.State == "free" || l.State == "dirty-free" { // a free lane keeps its last branch's task
					e.Free++
				} else {
					e.Busy++
				}
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

//go:embed page.html
var pageHTML []byte

// page is the one embedded HTML page: the page itself routes by the URL's hash.
func (s *Server) page(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		fail(w, http.StatusNotFound, "no such route: "+r.URL.Path)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(pageHTML)
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
