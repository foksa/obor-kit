package herdrapp_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"obor/internal/app/port"
	"obor/internal/herdrapp"
	"obor/internal/herdrapp/herdrapptest"
)

// one is the first space of the window (a test's precondition: the window has one).
func one(t *testing.T, app *herdrapptest.App) herdrapptest.Space {
	t.Helper()
	ss := app.Spaces()
	if len(ss) != 1 {
		t.Fatalf("precondition: the fake window has %d spaces, want 1", len(ss))
	}
	return ss[0]
}

// doCalls is the window's calls of `do <command>`, the command's name each.
func doCalls(app *herdrapptest.App) []string {
	var out []string
	for _, c := range app.Calls() {
		if len(c) >= 2 && c[0] == "do" {
			out = append(out, c[1])
		}
	}
	return out
}

// verbs is the calls' verbs, in order.
func verbs(app *herdrapptest.App) []string {
	var out []string
	for _, c := range app.Calls() {
		if len(c) > 0 {
			out = append(out, c[0])
		}
	}
	return out
}

func TestLabels(t *testing.T) {
	got := herdrapp.Labels("spa1", "tb2")
	want := map[string]string{"herdr.space": "spa1", "herdr.tab": "tb2"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Labels = %v, want %v", got, want)
	}
}

func TestSpaces(t *testing.T) {
	app := herdrapptest.New()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory: " + err.Error())
	}
	repo := filepath.Join(t.TempDir(), "obor-kit")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	app.AddSpace(herdrapptest.Space{ID: "spa1", Name: "obor-kit", Folder: filepath.Join(repo, "lane"),
		Tabs: []herdrapptest.Tab{{ID: "tb1", Name: "1"}, {ID: "tb2", Name: "lead"}}})
	app.AddSpace(herdrapptest.Space{ID: "spa2", Name: "~", Folder: home})
	app.AddSpace(herdrapptest.Space{ID: "spa3", Name: "api-remote", Folder: "/srv/api", Machine: "vps",
		Tabs: []herdrapptest.Tab{{ID: "tb3", Name: "1"}}})

	got, err := app.Client().Spaces(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("Spaces = %d spaces, want 3", len(got))
	}
	// A space named after its repo is the one the app would name; the lane's folder is under that repo.
	if got[0].Named {
		t.Errorf("space 1 Named = true: %q is the repo's auto name", got[0].Name)
	}
	if got[1].Named {
		t.Errorf("space 2 Named = true: %q is the home's auto name", got[1].Name)
	}
	if !got[2].Named {
		t.Errorf("space 3 Named = false: %q is not %q's auto name", got[2].Name, "api-remote")
	}
	if got[2].Machine != "vps" {
		t.Errorf("space 3 Machine = %q, want vps", got[2].Machine)
	}
	if tabs := got[0].Tabs; len(tabs) != 2 || tabs[0].Named || !tabs[1].Named {
		t.Errorf("space 1 tabs = %+v, want an unnamed \"1\" and a named \"lead\"", tabs)
	}
}

func TestSpacesReadsVersionOnce(t *testing.T) {
	app := herdrapptest.New()
	c := app.Client()
	for i := 0; i < 2; i++ {
		if _, err := c.Spaces(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if got := strings.Join(verbs(app), ","); got != "--version,ls,ls" {
		t.Errorf("calls = %s, want one --version and two ls", got)
	}
}

func TestEnsureSpaceCreates(t *testing.T) {
	app := herdrapptest.New()
	c := app.Client()
	folder := "/work/obor-kit"
	got, err := c.EnsureSpace(context.Background(), "obor-kit", folder)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "sp1" || got.Name != "obor-kit" || got.Folder != folder || !got.Named {
		t.Errorf("EnsureSpace = %+v, want the new space sp1", got)
	}
	if got.First.ID == "" || got.First.Terminal != "t1" {
		t.Errorf("EnsureSpace's first tab = %+v, want space.new's tab and the terminal it printed (t1)", got.First)
	}
	if calls := doCalls(app); !reflect.DeepEqual(calls, []string{"space.new"}) {
		t.Errorf("do calls = %v, want [space.new]", calls)
	}
	// A second call finds it: the same id and no second space.new.
	again, err := c.EnsureSpace(context.Background(), "obor-kit", folder)
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != got.ID || again.First.ID != "" {
		t.Errorf("second EnsureSpace = %+v, want %q found, no first tab made", again, got.ID)
	}
	if calls := doCalls(app); len(calls) != 1 {
		t.Errorf("do calls after the second EnsureSpace = %v, want one", calls)
	}
}

func TestEnsureSpaceRenamesUnnamed(t *testing.T) {
	app := herdrapptest.New()
	app.AddSpace(herdrapptest.Space{ID: "spa1", Name: "obor-kit", Folder: "/work/obor-kit",
		Tabs: []herdrapptest.Tab{{ID: "tb1", Name: "1"}}})
	got, err := app.Client().EnsureSpace(context.Background(), "My Project", "/work/obor-kit")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "spa1" || got.Name != "My Project" {
		t.Errorf("EnsureSpace = %+v, want spa1 renamed", got)
	}
	if calls := doCalls(app); !reflect.DeepEqual(calls, []string{"space.rename"}) {
		t.Errorf("do calls = %v, want [space.rename]", calls)
	}
	if name := one(t, app).Name; name != "My Project" {
		t.Errorf("the window's space is named %q, want My Project", name)
	}
}

func TestEnsureSpaceNamed(t *testing.T) {
	app := herdrapptest.New()
	app.AddSpace(herdrapptest.Space{ID: "spa1", Name: "My Project", Folder: "/work/obor-kit",
		Tabs: []herdrapptest.Tab{{ID: "tb1", Name: "1"}}})
	got, err := app.Client().EnsureSpace(context.Background(), "My Project", "/work/elsewhere")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "spa1" {
		t.Errorf("EnsureSpace = %+v, want the named space spa1", got)
	}
	if calls := doCalls(app); len(calls) != 0 {
		t.Errorf("do calls = %v, want none", calls)
	}
}

func TestEnsureSpaceSkipsRemote(t *testing.T) {
	app := herdrapptest.New()
	app.AddSpace(herdrapptest.Space{ID: "spa9", Name: "obor-kit", Folder: "/srv/obor-kit", Machine: "vps",
		Tabs: []herdrapptest.Tab{{ID: "tb9", Name: "1"}}})
	got, err := app.Client().EnsureSpace(context.Background(), "obor-kit", "/work/obor-kit")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID == "spa9" || got.ID != "sp1" {
		t.Errorf("EnsureSpace = %+v, want a new local space, not the vps one", got)
	}
	if calls := doCalls(app); !reflect.DeepEqual(calls, []string{"space.new"}) {
		t.Errorf("do calls = %v, want [space.new]", calls)
	}
}

func TestEnsureSpaceNeedsNameAndFolder(t *testing.T) {
	app := herdrapptest.New()
	for _, c := range []struct{ name, folder string }{{"", "/work/p"}, {"p", ""}} {
		if _, err := app.Client().EnsureSpace(context.Background(), c.name, c.folder); err == nil {
			t.Errorf("EnsureSpace(%q, %q) = nil error", c.name, c.folder)
		}
	}
	if calls := doCalls(app); len(calls) != 0 {
		t.Errorf("do calls = %v, want none", calls)
	}
}

func TestWindowClosed(t *testing.T) {
	app := herdrapptest.New()
	app.Down()
	_, err := app.Client().Spaces(context.Background())
	if err == nil {
		t.Fatal("Spaces with the window closed = nil error")
	}
	if !port.NotRunning(err) {
		t.Errorf("Spaces error %v is not port.NotRunning: the caller places by labels later", err)
	}
	if !strings.Contains(err.Error(), "no Herdr window") {
		t.Errorf("Spaces error = %q, want the CLI's no-window line", err)
	}
}

func TestErrorReply(t *testing.T) {
	app := herdrapptest.New()
	app.Fail("space.new", "folder /work/p is not there")
	_, err := app.Client().EnsureSpace(context.Background(), "p", "/work/p")
	if err == nil || !strings.Contains(err.Error(), "folder /work/p is not there") {
		t.Fatalf("EnsureSpace = %v, want the CLI's error reply", err)
	}
	if len(app.Spaces()) != 0 {
		t.Errorf("the window has a space after a failed space.new")
	}
}

func TestOldVersion(t *testing.T) {
	app := herdrapptest.New()
	app.SetVersion("0.1.15")
	_, err := app.Client().Spaces(context.Background())
	if err == nil {
		t.Fatal("Spaces of herdr-app 0.1.15 = nil error")
	}
	for _, want := range []string{"0.1.15", herdrapp.MinVersion, "update"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal %q does not name %q", err, want)
		}
	}
	if got := strings.Join(verbs(app), ","); got != "--version" {
		t.Errorf("calls = %s, want only --version before the refusal", got)
	}
}

func TestContract(t *testing.T) {
	have, err := herdrapp.CommandsOf(herdrapp.Contract())
	if err != nil {
		t.Fatal(err)
	}
	if len(have) < 60 {
		t.Fatalf("precondition: the committed copy has %d commands, not herdr-app's list", len(have))
	}
	for command, args := range herdrapp.Commands {
		got, ok := have[command]
		if !ok {
			t.Errorf("herdr-app %s has no command %s", herdrapp.MinVersion, command)
			continue
		}
		for _, a := range args {
			found := false
			for _, h := range got {
				found = found || h == a
			}
			if !found {
				t.Errorf("herdr-app %s's %s has no argument %s (it has %q)", herdrapp.MinVersion, command, a, got)
			}
		}
	}
}

func TestDrift(t *testing.T) {
	drift, err := herdrapp.Drift(herdrapp.Contract())
	if err != nil {
		t.Fatal(err)
	}
	if len(drift) != 0 {
		t.Errorf("Drift of the committed copy = %v, want none", drift)
	}
	installed := strings.Replace(string(herdrapp.Contract()), `"name": "folder"`, `"name": "directory"`, 1)
	drift, err = herdrapp.Drift([]byte(installed))
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(drift, "; ")
	for _, want := range []string{"space.new: no argument folder", "space.new: new argument directory"} {
		if !strings.Contains(got, want) {
			t.Errorf("Drift = %q, want it to name %q", got, want)
		}
	}
	renamed := strings.Replace(string(herdrapp.Contract()), `"name": "space.new"`, `"name": "space.make"`, 1)
	drift, err = herdrapp.Drift([]byte(renamed))
	if err != nil {
		t.Fatal(err)
	}
	got = strings.Join(drift, "; ")
	for _, want := range []string{"space.new: command gone", "space.make: new command"} {
		if !strings.Contains(got, want) {
			t.Errorf("Drift = %q, want it to name %q", got, want)
		}
	}
}

// NewTab makes a tab though the space has one of that name (its terminal's folder passed, the terminal the app
// printed in the answer); RenameSpace renames the local space named old and says
// false for a window with none.
func TestNewTabAndRenameSpace(t *testing.T) {
	app := herdrapptest.New()
	app.AddSpace(herdrapptest.Space{ID: "spa1", Name: "proj", Folder: "/p", Tabs: []herdrapptest.Tab{{ID: "tb1", Name: "lanes"}}})
	c, ctx := app.Client(), context.Background()
	tab, err := c.NewTab(ctx, "spa1", "lanes", "/p")
	if err != nil || tab.ID == "" || tab.ID == "tb1" || tab.Name != "lanes" {
		t.Fatalf("NewTab = %+v, %v; want a second tab named lanes", tab, err)
	}
	if got := one(t, app).Tabs; len(got) != 2 || got[1].Name != "lanes" {
		t.Errorf("tabs = %+v, want two lanes tabs", got)
	}
	calls := app.Calls()
	if last := calls[len(calls)-1]; tab.Terminal != "t1" || len(last) < 3 || last[1] != "tab.new" || !strings.Contains(last[2], `"folder":"/p"`) {
		t.Errorf("NewTab = %+v, last call %q; want terminal t1 and tab.new with folder /p", tab, last)
	}
	if ok, err := c.RenameSpace(ctx, "none", "x"); ok || err != nil {
		t.Errorf("RenameSpace of a space the window lacks = %v, %v; want false", ok, err)
	}
	if ok, err := c.RenameSpace(ctx, "proj", "nw"); !ok || err != nil || one(t, app).Name != "nw" {
		t.Errorf("RenameSpace = %v, %v; space %q, want renamed to nw", ok, err, one(t, app).Name)
	}
	app.Down()
	if _, err := c.NewTab(ctx, "spa1", "x", "/p"); !port.NotRunning(err) {
		t.Errorf("NewTab with the window closed = %v, want NotRunning", err)
	}
}

// space.new and tab.new answers that name no terminal are errors: the terminal the app started is known only by the
// address it printed, never guessed.
func TestNewTabAndSpaceNeedTheirTerminal(t *testing.T) {
	var calls []string
	c := &herdrapp.Client{Run: func(_ context.Context, args []string) (herdrapp.Reply, error) {
		calls = append(calls, strings.Join(args, " "))
		switch {
		case args[0] == "--version":
			return herdrapp.Reply{Stdout: "herdr-app " + herdrapp.MinVersion + "\n"}, nil
		case args[0] == "ls":
			return herdrapp.Reply{Stdout: `{"spaces":[]}` + "\n"}, nil
		case len(args) > 1 && args[1] == "space.new":
			return herdrapp.Reply{Stdout: `{"space":"sp1","tab":"tb1"}` + "\n"}, nil
		}
		return herdrapp.Reply{Stdout: `{"tab":"tb2"}` + "\n"}, nil
	}}
	ctx := context.Background()
	if _, err := c.EnsureSpace(ctx, "p", "/work/p"); err == nil || !strings.Contains(err.Error(), "no tab or terminal") {
		t.Errorf("EnsureSpace of an answer with no terminal = %v, want the error", err)
	}
	if _, err := c.NewTab(ctx, "sp1", "lanes", "/work/p"); err == nil || !strings.Contains(err.Error(), "no tab or terminal") {
		t.Errorf("NewTab of an answer with no terminal = %v, want the error", err)
	}
	if !slices.ContainsFunc(calls, func(c string) bool { return strings.HasPrefix(c, "do tab.new") }) {
		t.Errorf("calls %q, want tab.new asked", calls)
	}
}
