package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"obor/internal/app/wire"
)

// T-753: obor web's two verbs. A POST is taken only on /api/config and /api/pause, and only from the page itself (its
// Origin, the listen address as Host, a JSON body); each is one request to the project's serve.

// callSpy is a Server.Call that records each request and answers reply (or err).
type callSpy struct {
	mu    sync.Mutex
	calls []spied
	reply string
	err   error
}

type spied struct {
	root, method string
	c            wire.Call
}

func (s *callSpy) call(_ context.Context, root, method string, c wire.Call) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, spied{root, method, c})
	return s.reply, s.err
}

func (s *callSpy) all() []spied {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]spied(nil), s.calls...)
}

// post is a POST of body to url as the page sends it from host (Origin http://<host>, JSON), changed by edit.
func post(h http.Handler, url, body, host string, edit ...func(*http.Request)) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, url, strings.NewReader(body))
	r.Host = host
	r.Header.Set("Origin", "http://"+host)
	r.Header.Set("Content-Type", "application/json")
	for _, e := range edit {
		e(r)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// A POST from the page reaches serve; one without its Origin, from another origin, to another Host (DNS rebinding)
// or with a body that is not JSON is 403 and asks serve nothing. localhost and 127.0.0.1 are one host.
func TestPostOnlyFromThePage(t *testing.T) {
	p, _, s := fixture(t)
	spy := &callSpy{reply: "SET lanes.L-01 \"\" → \"opus\""}
	s.Call, s.Listen = spy.call, "127.0.0.1:8787"
	h := s.Handler()
	const body = `{"changes":[{"key":"lanes.L-01","value":"opus"}],"base":"r1"}`
	w := post(h, "/api/config?project=pj", body, "127.0.0.1:8787")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"reply":"SET lanes.L-01`) {
		t.Fatalf("the page's POST: %d %s", w.Code, w.Body.String())
	}
	calls := spy.all()
	if len(calls) != 1 || calls[0].method != wire.MethodConfig || calls[0].root != p.Root || len(calls[0].c.Changes) != 1 ||
		calls[0].c.Changes[0].Key != "lanes.L-01" || string(calls[0].c.Changes[0].Value) != `"opus"` ||
		calls[0].c.Base != "r1" || calls[0].c.Source != "web" || calls[0].c.CallID == "" ||
		calls[0].c.Agent != "" || calls[0].c.Role != "" || calls[0].c.Root != p.Root {
		t.Fatalf("serve was asked %+v, want one config_set of lanes.L-01 from the owner on the web", calls)
	}
	if w := post(h, "/api/config?project=pj", body, "localhost:8787"); w.Code != http.StatusOK {
		t.Errorf("from localhost:8787 (the same host): %d %s", w.Code, w.Body.String())
	}
	asked := len(spy.all())
	for _, c := range []struct {
		name string
		host string
		edit func(*http.Request)
	}{
		{"no Origin", "127.0.0.1:8787", func(r *http.Request) { r.Header.Del("Origin") }},
		{"another origin", "127.0.0.1:8787", func(r *http.Request) { r.Header.Set("Origin", "http://evil.example") }},
		{"another port", "127.0.0.1:8787", func(r *http.Request) { r.Header.Set("Origin", "http://127.0.0.1:9999") }},
		{"https origin", "127.0.0.1:8787", func(r *http.Request) { r.Header.Set("Origin", "https://127.0.0.1:8787") }},
		{"rebinding Host", "evil.example:8787", nil},
		{"another Host port", "127.0.0.1:9999", nil},
		{"form body", "127.0.0.1:8787", func(r *http.Request) { r.Header.Set("Content-Type", "application/x-www-form-urlencoded") }},
		{"text body", "127.0.0.1:8787", func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }},
		{"no content type", "127.0.0.1:8787", func(r *http.Request) { r.Header.Del("Content-Type") }},
	} {
		edit := []func(*http.Request){}
		if c.edit != nil {
			edit = append(edit, c.edit)
		}
		for _, url := range []string{"/api/config?project=pj", "/api/pause?project=pj"} {
			if w := post(h, url, body, c.host, edit...); w.Code != http.StatusForbidden {
				t.Errorf("%s, %s: %d %s, want 403", c.name, url, w.Code, w.Body.String())
			}
		}
	}
	if got := len(spy.all()); got != asked {
		t.Errorf("a refused POST asked serve: %d calls, want %d", got, asked)
	}
	w = post(h, "/api/config?project=pj", body, "127.0.0.1:8787", func(r *http.Request) {
		r.Header.Set("Content-Type", "application/json; charset=utf-8")
	})
	if w.Code != http.StatusOK {
		t.Errorf("application/json with a charset: %d, want 200", w.Code)
	}
}

// POST anywhere but the two routes is 405, as is any other method on them; the GET routes answer as before.
func TestPostElsewhereIs405(t *testing.T) {
	_, _, s := fixture(t)
	spy := &callSpy{}
	s.Call = spy.call
	h := s.Handler()
	for _, url := range []string{"/api/view?project=pj", "/api/setup?project=pj", "/api/projects", "/", "/api/events?project=pj"} {
		if w := post(h, url, `{}`, DefaultListen); w.Code != http.StatusMethodNotAllowed {
			t.Errorf("POST %s: %d, want 405", url, w.Code)
		}
	}
	for _, m := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		for _, url := range []string{"/api/config?project=pj", "/api/pause?project=pj"} {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(m, url, nil))
			if w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != "POST" {
				t.Errorf("%s %s: %d (Allow %q), want 405 with Allow POST", m, url, w.Code, w.Header().Get("Allow"))
			}
		}
	}
	for _, url := range []string{"/api/view?project=pj", "/api/setup?project=pj", "/api/projects", "/"} {
		if w := get(t, h, url); w.Code != http.StatusOK {
			t.Errorf("GET %s: %d, want 200", url, w.Code)
		}
	}
	if len(spy.all()) != 0 {
		t.Errorf("serve was asked %+v", spy.all())
	}
}

// Serve not running: the POST is 503 with the reason (the default call, no socket at the project's root).
func TestPostServeDownIs503(t *testing.T) {
	_, _, s := fixture(t)
	h := s.Handler()
	for _, c := range []struct{ url, body string }{
		{"/api/config?project=pj", `{"changes":[{"key":"lanes.L-01","value":"opus"}],"base":"r1"}`},
		{"/api/pause?project=pj", `{"reason":"lunch"}`},
	} {
		w := post(h, c.url, c.body, DefaultListen)
		if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "obor serve is not running") {
			t.Errorf("%s with serve down: %d %s, want 503 naming serve", c.url, w.Code, w.Body.String())
		}
	}
	spy := &callSpy{err: errors.New("obor serve is not running (dial unix x: connect: connection refused)")}
	s.Call = func(ctx context.Context, root, method string, c wire.Call) (string, error) {
		return "", errors.Join(errServeDown, spy.err)
	}
	if w := post(s.Handler(), "/api/pause?project=pj", `{"clear":true}`, DefaultListen); w.Code != http.StatusServiceUnavailable {
		t.Errorf("a refused socket: %d, want 503", w.Code)
	}
}

// The pause from the web: set with a reason and a duration, and clear, each serve's pause call from the owner; a bad
// duration or no reason is 400 and asks serve nothing; serve's refusal is 400 with its reason.
func TestPauseViaWeb(t *testing.T) {
	p, _, s := fixture(t)
	spy := &callSpy{reply: "PAUSED until 13:30"}
	s.Call = spy.call
	h := s.Handler()
	before := time.Now()
	w := post(h, "/api/pause?project=pj", `{"reason":" lunch ","for":"30m"}`, DefaultListen)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "PAUSED until 13:30") {
		t.Fatalf("pause: %d %s", w.Code, w.Body.String())
	}
	spy.reply = "PAUSE CLEARED"
	if w := post(h, "/api/pause?project=pj", `{"clear":true}`, DefaultListen); w.Code != http.StatusOK {
		t.Fatalf("clear: %d %s", w.Code, w.Body.String())
	}
	calls := spy.all()
	if len(calls) != 2 {
		t.Fatalf("serve was asked %+v, want a pause and a clear", calls)
	}
	set, clear := calls[0], calls[1]
	until := before.Add(30 * time.Minute).Unix()
	if set.method != wire.MethodPause || set.root != p.Root || set.c.Note != "lunch" || set.c.Clear ||
		set.c.Until < until || set.c.Until > until+5 || set.c.CallID == "" || set.c.Role != "" {
		t.Errorf("the pause call %+v, want the owner's, lunch, until in 30m", set.c)
	}
	if clear.method != wire.MethodPause || !clear.c.Clear || clear.c.CallID == "" || clear.c.CallID == set.c.CallID {
		t.Errorf("the clear call %+v, want clear with its own call id", clear.c)
	}
	for _, body := range []string{`{"reason":"x","for":"soon"}`, `{"reason":"x","for":"-5m"}`, `{"reason":"  "}`,
		`{"reason":"x","until":3}`, `not json`} {
		if w := post(h, "/api/pause?project=pj", body, DefaultListen); w.Code != http.StatusBadRequest {
			t.Errorf("pause %s: %d %s, want 400", body, w.Code, w.Body.String())
		}
	}
	if got := len(spy.all()); got != 2 {
		t.Errorf("a bad body asked serve: %d calls", got)
	}
	spy.err = errors.New("config: lane L-01 has task T-1: change its type once the lane is free")
	w = post(h, "/api/config?project=pj", `{"changes":[{"key":"lanes.L-01","value":"opus"}],"base":"r1"}`, DefaultListen)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "lane L-01 has task T-1") {
		t.Errorf("serve's refusal: %d %s, want 400 with it", w.Code, w.Body.String())
	}
	for _, body := range []string{`{"changes":[{"key":"lanes.L-01"}],"base":"r1"}`, `{"changes":[],"base":"r1"}`,
		`{"changes":[{"key":"lanes.L-01","value":"opus"}]}`, `{"key":"lanes.L-01","value":"opus"}`,
		`{"changes":[{"value":"opus"}],"base":"r1"}`} {
		if w := post(h, "/api/config?project=pj", body, DefaultListen); w.Code != http.StatusBadRequest {
			t.Errorf("config %s: %d, want 400 (changes with a key and a value, and the base, are required)", body, w.Code)
		}
	}
	if w := post(h, "/api/config?project=nope", `{"changes":[{"key":"lanes.L-01","value":"opus"}],"base":"r1"}`, DefaultListen); w.Code != http.StatusNotFound {
		t.Errorf("an unknown project: %d, want 404", w.Code)
	}
}
