// Package herdrapptest is a herdr-app window held in memory for the herdrapp client's tests, hctest's counterpart: it
// holds spaces and tabs, answers `ls`, `do`, `commands` and `--version` through Client.Run in the JSON the app prints
// (herdr-app 0.1.16), and records every call a test's client made. Tests never start the real app or drive its window.
package herdrapptest

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"sync"

	"obor/internal/herdrapp"
)

// Space is a space of the fake window, its tabs in order.
type Space struct {
	ID      string
	Name    string
	Folder  string
	Machine string // "" is this computer
	Tabs    []Tab
}

// Tab is a tab of a space.
type Tab struct {
	ID   string
	Name string
}

// App is one window. Its fields are read under its lock: set them through its methods.
type App struct {
	mu      sync.Mutex
	version string
	down    bool
	fails   map[string]string // command → its {"error": …} reply
	spaces  []Space
	calls   [][]string
	seq     int
	start   func(space, tab, folder string) string // the terminal core a new tab's terminal starts on
}

// New is a window of herdr-app MinVersion answering obor's committed `herdr-app commands` copy, with no spaces.
func New() *App {
	return &App{version: herdrapp.MinVersion, fails: map[string]string{}}
}

// Client is a client of the window: its Run answers this window in memory.
func (a *App) Client() *herdrapp.Client {
	return &herdrapp.Client{Run: a.Run}
}

// StartTerminals bridges the window to a terminal core (hctest.Core.AppTerminals): space.new and tab.new start their
// tab's first terminal through start, as herdr-app does on hc, and answer its address. With none they answer t1 and
// no terminal exists.
func (a *App) StartTerminals(start func(space, tab, folder string) string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.start = start
}

// AddSpace adds a space with its tabs, as the app would list it.
func (a *App) AddSpace(s Space) {
	a.mu.Lock()
	defer a.mu.Unlock()
	s.Tabs = slices.Clone(s.Tabs)
	a.spaces = append(a.spaces, s)
}

// Spaces is the window's spaces as they are now.
func (a *App) Spaces() []Space {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.spaces)
}

// SetVersion makes `--version` answer version.
func (a *App) SetVersion(v string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.version = v
}

// Down closes the window: every call answers the CLI's no-window line with exit 2, as it does with the app quit.
func (a *App) Down() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.down = true
}

// Up opens the window again after Down.
func (a *App) Up() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.down = false
}

// Fail makes command answer msg as its {"error": …} reply, exit 1; a call that would change the window changes
// nothing.
func (a *App) Fail(command, msg string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.fails[command] = msg
}

// Calls is every call the window answered, each the CLI's arguments ("ls", "do", "space.new", …); the app's own
// program name is not among them.
func (a *App) Calls() [][]string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.calls)
}

// Run answers one CLI call: the client's Run.
func (a *App) Run(_ context.Context, args []string) (herdrapp.Reply, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls = append(a.calls, slices.Clone(args))
	if len(args) == 0 {
		return usage(), nil
	}
	switch args[0] {
	case "--version":
		return ok("herdr-app " + a.version + "\n"), nil
	case "commands":
		return ok(string(herdrapp.Contract())), nil
	case "ls":
		if a.down {
			return a.noWindow(), nil
		}
		return ok(a.list()), nil
	case "do":
		if a.down {
			return a.noWindow(), nil
		}
		if len(args) < 2 {
			return usage(), nil
		}
		return ok(a.do(args[1], args[2:])), nil
	}
	return usage(), nil
}

// list is `herdr-app ls`' answer: spaces, their tabs and terminals.
func (a *App) list() string {
	type terminal struct {
		Terminal  string `json:"terminal"`
		Pane      string `json:"pane"`
		Status    any    `json:"status"`
		App       any    `json:"app"`
		Name      any    `json:"name"`
		SubAgents []any  `json:"sub_agents"`
	}
	type tab struct {
		ID        string     `json:"id"`
		Name      string     `json:"name"`
		Active    bool       `json:"active"`
		Settled   bool       `json:"settled"`
		Terminals []terminal `json:"terminals"`
	}
	type space struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Folder  string `json:"folder"`
		Machine any    `json:"machine"`
		Active  bool   `json:"active"`
		Tabs    []tab  `json:"tabs"`
	}
	out := struct {
		Spaces []space `json:"spaces"`
	}{Spaces: []space{}}
	for _, s := range a.spaces {
		var machine any
		if s.Machine != "" {
			machine = s.Machine
		}
		sp := space{ID: s.ID, Name: s.Name, Folder: s.Folder, Machine: machine, Active: s.ID == a.spaces[0].ID}
		for i, t := range s.Tabs {
			sp.Tabs = append(sp.Tabs, tab{ID: t.ID, Name: t.Name, Active: i == 0, Settled: false,
				Terminals: []terminal{{Terminal: "t" + strconv.Itoa(i+1), Pane: "p" + strconv.Itoa(i+1)}}})
		}
		out.Spaces = append(out.Spaces, sp)
	}
	b, _ := json.Marshal(out)
	return string(b) + "\n"
}

// do is one `herdr-app do <command> '<json>'` answer.
func (a *App) do(command string, rest []string) string {
	if msg, ok := a.fails[command]; ok {
		return errorReply(msg)
	}
	arg := map[string]any{}
	if len(rest) > 0 {
		if err := json.Unmarshal([]byte(rest[0]), &arg); err != nil {
			return errorReply("bad arguments: " + err.Error())
		}
	}
	str := func(k string) string { s, _ := arg[k].(string); return s }
	switch command {
	case "space.new":
		id := a.nextID("sp")
		tab := Tab{ID: a.nextID("tb"), Name: "1"}
		a.spaces = append(a.spaces, Space{ID: id, Name: str("name"), Folder: str("folder"), Tabs: []Tab{tab}})
		return fmt.Sprintf(`{"space":%q,"tab":%q,"terminal":%q}`, id, tab.ID, a.terminal(id, tab.ID, str("folder")))
	case "space.rename":
		s := a.space(str("id"))
		if s == nil {
			return errorReply("no space " + str("id"))
		}
		s.Name = str("name")
		return fmt.Sprintf(`{"space":%q}`, s.ID)
	case "tab.new":
		s := a.space(str("space"))
		if s == nil {
			return errorReply("no space " + str("space"))
		}
		tab := Tab{ID: a.nextID("tb"), Name: str("name")}
		s.Tabs = append(s.Tabs, tab)
		return fmt.Sprintf(`{"tab":%q,"terminal":%q}`, tab.ID, a.terminal(s.ID, tab.ID, cmp.Or(str("folder"), s.Folder)))
	case "tab.rename":
		for i := range a.spaces {
			for j := range a.spaces[i].Tabs {
				if a.spaces[i].Tabs[j].ID == str("id") {
					a.spaces[i].Tabs[j].Name = str("name")
					return fmt.Sprintf(`{"tab":%q}`, str("id"))
				}
			}
		}
		return errorReply("no tab " + str("id"))
	}
	return errorReply("unknown command " + command)
}

// terminal starts a new tab's first terminal, under the lock: its address ("t1" with no core bridged).
func (a *App) terminal(space, tab, folder string) string {
	if a.start == nil {
		return "t1"
	}
	return a.start(space, tab, folder)
}

// space is the space with id, under the lock (nil when the window has none).
func (a *App) space(id string) *Space {
	for i := range a.spaces {
		if a.spaces[i].ID == id {
			return &a.spaces[i]
		}
	}
	return nil
}

// nextID is a fresh id with prefix, never one the window holds.
func (a *App) nextID(prefix string) string {
	for {
		a.seq++
		id := prefix + strconv.Itoa(a.seq)
		used := false
		for _, s := range a.spaces {
			used = used || s.ID == id
			for _, t := range s.Tabs {
				used = used || t.ID == id
			}
		}
		if !used {
			return id
		}
	}
}

// noWindow is the CLI's answer with the app quit: the same line and exit code herdr-app 0.1.16 gives.
func (a *App) noWindow() herdrapp.Reply {
	return herdrapp.Reply{Stderr: "herdr-app: no Herdr window at " + a.home() + "/herdr-app.sock: " +
		"No such file or directory (os error 2)\n", Code: 2}
}

// home is the fake window's home, for the no-window line's path.
func (a *App) home() string { return "/tmp/herdr-app-test" }

func ok(stdout string) herdrapp.Reply { return herdrapp.Reply{Stdout: stdout} }

func usage() herdrapp.Reply {
	return herdrapp.Reply{Stderr: "herdr-app: usage: herdr-app ls | herdr-app commands | herdr-app status | " +
		"herdr-app do <command> [--arg value ... | json]\n", Code: 2}
}

func errorReply(msg string) string {
	b, _ := json.Marshal(map[string]string{"error": msg})
	return string(b) + "\n"
}
