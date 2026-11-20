package herdrapp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"

	"obor/internal/project"
)

// Space is a space of the window (`herdr-app ls`): its id and name, the folder its new terminals start in, whether
// someone named it, the machine it lives on ("" is this computer) and its tabs. First is the tab `space.new` made
// with the space when this call made it, its Terminal the one herdr-app started there; zero for a space that was
// there (its tabs are in Tabs).
type Space struct {
	ID      string
	Name    string
	Folder  string
	Named   bool
	Machine string
	Tabs    []Tab
	First   Tab
}

// Tab is a tab of a space: its id, its name and whether someone named it (an unnamed tab is called by its number).
// Terminal is the hc terminal herdr-app started in the tab when this call made it (`tab.new`, `space.new`): the
// address the command printed; "" for a tab that was there.
type Tab struct {
	ID       string
	Name     string
	Named    bool
	Terminal string
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
// name, else a new one (`space.new {name, folder}`, its First tab and that tab's terminal as the command printed
// them; an answer without either is an error). A second call finds the first's space and runs no command; a remote
// machine's space is never taken.
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
	switch {
	case r.Space == "":
		return Space{}, &Error{Op: "space.new", Msg: "no space in the answer"}
	case r.Tab == "" || r.Terminal == "":
		return Space{}, &Error{Op: "space.new", Msg: "no tab or terminal in the answer"}
	}
	return Space{ID: r.Space, Name: name, Folder: folder, Named: true,
		First: Tab{ID: r.Tab, Name: "1", Terminal: r.Terminal}}, nil
}

// NewTab is a new tab named name in the space (`tab.new {name, space, folder}`), though the space has one of that
// name: a second `lanes` tab when the first is full. herdr-app starts the tab's first terminal in folder ("": the
// app's default); the answer carries that terminal's address as the command printed it (Terminal; an answer without
// it is an error).
func (c *Client) NewTab(ctx context.Context, space, name, folder string) (Tab, error) {
	if space == "" || name == "" {
		return Tab{}, &Error{Op: "tab", Msg: "a new tab needs a space and a name"}
	}
	if err := c.check(ctx); err != nil {
		return Tab{}, err
	}
	args := map[string]any{"name": name, "space": space}
	if folder != "" {
		args["folder"] = folder
	}
	r, err := c.do(ctx, "tab.new", args)
	if err != nil {
		return Tab{}, err
	}
	if r.Tab == "" || r.Terminal == "" {
		return Tab{}, &Error{Op: "tab.new", Msg: "no tab or terminal in the answer"}
	}
	return Tab{ID: r.Tab, Name: name, Named: true, Terminal: r.Terminal}, nil
}

// RenameTab names the tab id name (`tab.rename`).
func (c *Client) RenameTab(ctx context.Context, id, name string) (Tab, error) {
	if _, err := c.do(ctx, "tab.rename", map[string]any{"id": id, "name": name}); err != nil {
		return Tab{}, err
	}
	return Tab{ID: id, Name: name, Named: true}, nil
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

// NameSpace names the space id name (`space.rename`).
func (c *Client) NameSpace(ctx context.Context, id, name string) error {
	_, err := c.do(ctx, "space.rename", map[string]any{"id": id, "name": name})
	return err
}

// CloseSpace closes the space with this id and its tabs (`space.close`, confirmed: the app asks no question).
func (c *Client) CloseSpace(ctx context.Context, id string) error {
	if id == "" {
		return &Error{Op: "space.close", Msg: "a closed space needs its id"}
	}
	if err := c.check(ctx); err != nil {
		return err
	}
	_, err := c.do(ctx, "space.close", map[string]any{"id": id, "confirmed": true})
	return err
}

// CloseTab closes the tab with this id (`tab.close`, confirmed).
func (c *Client) CloseTab(ctx context.Context, id string) error {
	if id == "" {
		return &Error{Op: "tab.close", Msg: "a closed tab needs its id"}
	}
	if err := c.check(ctx); err != nil {
		return err
	}
	_, err := c.do(ctx, "tab.close", map[string]any{"id": id, "confirmed": true})
	return err
}

// ids are what a `do` command printed of what it made: space.new and tab.new print the terminal they started too.
type ids struct {
	Space    string `json:"space"`
	Tab      string `json:"tab"`
	Terminal string `json:"terminal"`
	Error    string `json:"error"`
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
