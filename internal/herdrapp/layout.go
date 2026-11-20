package herdrapp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"obor/internal/project"
)

// Space is a space of the window (`herdr-app ls`): its id and name, the folder its new terminals start in, whether
// someone named it, the machine it lives on ("" is this computer) and its tabs.
type Space struct {
	ID      string
	Name    string
	Folder  string
	Named   bool
	Machine string
	Tabs    []Tab
}

// Tab is a tab of a space: its id, its name and whether someone named it (an unnamed tab is called by its number).
type Tab struct {
	ID    string
	Name  string
	Named bool
}

// Labels is a terminal's label pair for a tab: the label map that makes a terminal of this machine show in that tab
// (herdr-app's guide, docs/research/hc-probes.md P7).
func Labels(space, tab string) map[string]string {
	return map[string]string{LabelSpace: space, LabelTab: tab}
}

// Spaces is the window's spaces and tabs, remote machines' included (their Machine says so). The app prints no named
// flag, so Named is read from the name: a space someone named differs from the one the app gives it (its repo, else
// its folder, "~" at home — read from the space's folder, where the app reads its first terminal's), and an unnamed
// tab is called by its number.
func (c *Client) Spaces(ctx context.Context) ([]Space, error) {
	if err := c.check(ctx); err != nil {
		return nil, err
	}
	r, err := c.call(ctx, "ls", "ls")
	if err != nil {
		return nil, err
	}
	if r.Code != 0 {
		return nil, failure("ls", r)
	}
	var list struct {
		Spaces []struct {
			ID      string `json:"id"`
			Name    string `json:"name"`
			Folder  string `json:"folder"`
			Machine string `json:"machine"`
			Tabs    []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"tabs"`
		} `json:"spaces"`
	}
	if err := json.Unmarshal([]byte(r.Stdout), &list); err != nil {
		msg := line(r.Stdout)
		if msg == "" {
			msg = line(r.Stderr)
		}
		return nil, &Error{Op: "ls", Msg: "unreadable answer: " + msg}
	}
	out := make([]Space, 0, len(list.Spaces))
	for _, s := range list.Spaces {
		if s.ID == "" {
			return nil, &Error{Op: "ls", Msg: "a space with no id"}
		}
		space := Space{ID: s.ID, Name: s.Name, Folder: s.Folder, Machine: s.Machine, Named: s.Name != autoName(s.Folder)}
		for i, t := range s.Tabs {
			if t.ID == "" {
				return nil, &Error{Op: "ls", Msg: "a tab with no id"}
			}
			space.Tabs = append(space.Tabs, Tab{ID: t.ID, Name: t.Name, Named: t.Name != strconv.Itoa(i+1)})
		}
		out = append(out, space)
	}
	return out, nil
}

// EnsureSpace is the project's space: the local space named name, else an unnamed local space at folder renamed to
// name, else a new one (`space.new {name, folder}`). A second call finds the first's space and runs no command; a
// remote machine's space is never taken.
func (c *Client) EnsureSpace(ctx context.Context, name, folder string) (Space, error) {
	if name == "" || folder == "" {
		return Space{}, &Error{Op: "space", Msg: "an ensured space needs a name and a folder"}
	}
	spaces, err := c.Spaces(ctx)
	if err != nil {
		return Space{}, err
	}
	for _, s := range spaces {
		if s.Machine == "" && s.Name == name {
			return s, nil
		}
	}
	for _, s := range spaces {
		if s.Machine != "" || s.Named || !sameFolder(s.Folder, folder) {
			continue
		}
		if _, err := c.do(ctx, "space.rename", map[string]any{"id": s.ID, "name": name}); err != nil {
			return Space{}, err
		}
		s.Name, s.Named = name, true
		return s, nil
	}
	r, err := c.do(ctx, "space.new", map[string]any{"name": name, "folder": folder})
	if err != nil {
		return Space{}, err
	}
	if r.Space == "" {
		return Space{}, &Error{Op: "space.new", Msg: "no space in the answer"}
	}
	return Space{ID: r.Space, Name: name, Folder: folder, Named: true}, nil
}

// EnsureTab is the space's tab named name: the tab itself, else the space's one unnamed tab renamed (`tab.rename`),
// else a new tab (`tab.new {name, space}`). A second call finds the first's tab and runs no command.
func (c *Client) EnsureTab(ctx context.Context, space, name string) (Tab, error) {
	if space == "" || name == "" {
		return Tab{}, &Error{Op: "tab", Msg: "an ensured tab needs a space and a name"}
	}
	spaces, err := c.Spaces(ctx)
	if err != nil {
		return Tab{}, err
	}
	for _, s := range spaces {
		if s.ID != space {
			continue
		}
		for _, t := range s.Tabs {
			if t.Name == name {
				return t, nil
			}
		}
		if len(s.Tabs) == 1 && !s.Tabs[0].Named {
			if _, err := c.do(ctx, "tab.rename", map[string]any{"id": s.Tabs[0].ID, "name": name}); err != nil {
				return Tab{}, err
			}
			return Tab{ID: s.Tabs[0].ID, Name: name, Named: true}, nil
		}
		r, err := c.do(ctx, "tab.new", map[string]any{"name": name, "space": space})
		if err != nil {
			return Tab{}, err
		}
		if r.Tab == "" {
			return Tab{}, &Error{Op: "tab.new", Msg: "no tab in the answer"}
		}
		return Tab{ID: r.Tab, Name: name, Named: true}, nil
	}
	return Tab{}, &Error{Op: "tab", Msg: fmt.Sprintf("space %s is not in the window", space)}
}

// NewTab is a new tab named name in the space (`tab.new {name, space}`), though the space has one of that name: a
// second `lanes` tab when the first is full. EnsureTab finds a tab; this makes one.
func (c *Client) NewTab(ctx context.Context, space, name string) (Tab, error) {
	if space == "" || name == "" {
		return Tab{}, &Error{Op: "tab", Msg: "a new tab needs a space and a name"}
	}
	if err := c.check(ctx); err != nil {
		return Tab{}, err
	}
	r, err := c.do(ctx, "tab.new", map[string]any{"name": name, "space": space})
	if err != nil {
		return Tab{}, err
	}
	if r.Tab == "" {
		return Tab{}, &Error{Op: "tab.new", Msg: "no tab in the answer"}
	}
	return Tab{ID: r.Tab, Name: name, Named: true}, nil
}

// RenameSpace renames this computer's space named old to name (`space.rename`); false when the window has no such
// space (nothing to rename) or already has one named name and none named old.
func (c *Client) RenameSpace(ctx context.Context, old, name string) (bool, error) {
	spaces, err := c.Spaces(ctx)
	if err != nil {
		return false, err
	}
	for _, s := range spaces {
		if s.Machine == "" && s.Name == old {
			if _, err := c.do(ctx, "space.rename", map[string]any{"id": s.ID, "name": name}); err != nil {
				return false, err
			}
			return true, nil
		}
	}
	return false, nil
}

// ids are what a `do` command printed of what it made.
type ids struct {
	Space string `json:"space"`
	Tab   string `json:"tab"`
	Error string `json:"error"`
}

// do runs one command as `herdr-app do <command> '<json>'`: the ids it printed, its {"error": …} reply as an error.
func (c *Client) do(ctx context.Context, command string, args map[string]any) (ids, error) {
	b, err := json.Marshal(args)
	if err != nil {
		return ids{}, &Error{Op: command, Msg: err.Error()}
	}
	r, err := c.call(ctx, command, "do", command, string(b))
	if err != nil {
		return ids{}, err
	}
	var got ids
	if json.Unmarshal([]byte(r.Stdout), &got) != nil {
		if r.Code != 0 {
			return ids{}, failure(command, r)
		}
		return ids{}, &Error{Op: command, Msg: "unreadable answer: " + line(r.Stdout)}
	}
	if got.Error != "" {
		return ids{}, &Error{Op: command, Msg: got.Error}
	}
	if r.Code != 0 {
		return ids{}, failure(command, r)
	}
	return got, nil
}

// autoName is the name herdr-app gives a space nobody named: where its first terminal is — its repo, else its folder,
// "~" at home.
func autoName(folder string) string {
	if folder == "" {
		return ""
	}
	dir := project.Physical(folder)
	if home, err := os.UserHomeDir(); err == nil && home != "" && dir == project.Physical(home) {
		return "~"
	}
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return filepath.Base(d)
		}
		if parent := filepath.Dir(d); parent == d {
			break
		}
	}
	return filepath.Base(dir)
}

// sameFolder: a and b name the same directory (a symlinked path either way is the same folder).
func sameFolder(a, b string) bool {
	return a != "" && b != "" && project.Physical(a) == project.Physical(b)
}
