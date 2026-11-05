package main

import (
	"testing"

	"obor/internal/app"
	"obor/internal/project"
	"obor/internal/testkit"
)

// T-646: obor projects lists the projects this machine knows by name, by name: "<name> <root> serve <up|down>", a
// stale entry left out (its root now holds another project); serve is up while the project's serve holds its lock.
func TestProjectsListsKnownProjects(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	p.Config()
	q := testkit.New(t)
	q.Config(testkit.Set("name", "other"), testkit.Set("prefix", "ot"))
	r := testkit.New(t)
	r.Config(testkit.Set("name", "gone"), testkit.Set("prefix", "gn"))
	home := t.TempDir()
	env := []string{"OBOR_STATE_HOME=" + home}
	penv := p.Env("OBOR_STATE_HOME=" + home)

	if rc, stdout, _ := run(t, p.Root, env, "projects"); rc != 0 ||
		stdout != "no obor projects known yet (obor serve and obor init register one)\n" {
		t.Fatalf("no project registered: exit %d, %q", rc, stdout)
	}
	for _, root := range []string{p.Root, q.Root, r.Root} {
		if _, err := project.Register(root, penv); err != nil {
			t.Fatal(err)
		}
	}
	r.Config(testkit.Set("name", "another")) // the root is there, its project is not the "gone" the list names
	if rc, stdout, _ := run(t, p.Root, env, "projects", "extra"); rc != 1 ||
		stdout != "FAIL usage: obor projects\n" {
		t.Errorf("obor projects with an argument: exit %d, %q", rc, stdout)
	}

	// other's serve holds its lock; none runs for proj.
	lock := project.NewLock(app.FlockFile(q.Root))
	held, err := lock.Try()
	if err != nil || !held {
		t.Fatalf("precondition: holding other's serve lock: held %v, %v", held, err)
	}
	defer lock.Unlock()
	want := "other " + q.Root + " serve up\nproj " + p.Root + " serve down\n"
	if rc, stdout, _ := run(t, p.Root, env, "projects"); rc != 0 || stdout != want {
		t.Errorf("obor projects: exit %d\n%q\nwant %q", rc, stdout, want)
	}
}
