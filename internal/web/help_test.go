package web

import (
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"obor/internal/help"
)

// T-854: /api/help is the command table (help.Groups, help.Commands) and the kit's README "Install" and "Set up a
// project" sections, read at the request: a README changed after the start shows its new text.
func TestHelpAPIIsTheTableAndReadmeSections(t *testing.T) {
	t.Parallel()
	kit := t.TempDir()
	readme := filepath.Join(kit, "README.md")
	write := func(install string) {
		text := "# obor\n\n## Install\n\n" + install + "\n\n```sh\n## not a heading in a code block\n```\n\n" +
			"## Set up a project\n\nrun `obor init`\n\n## Everyday commands\n\nnot shown\n"
		if err := os.WriteFile(readme, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("curl one")
	h := (&Server{Kit: kit}).Handler()

	r := get(t, h, "/api/help")
	if r.Code != http.StatusOK {
		t.Fatalf("/api/help: %d %s", r.Code, r.Body.String())
	}
	var got Help
	decode(t, r, &got)
	if !reflect.DeepEqual(got.Groups, help.Groups) || !reflect.DeepEqual(got.Commands, help.Commands) {
		t.Errorf("/api/help table: %v, %d commands; want help.Groups and its %d commands", got.Groups, len(got.Commands),
			len(help.Commands))
	}
	want := []help.Section{
		{Title: "Install", Body: "curl one\n\n```sh\n## not a heading in a code block\n```"},
		{Title: "Set up a project", Body: "run `obor init`"},
	}
	if !reflect.DeepEqual(got.Sections, want) {
		t.Errorf("/api/help sections: %q; want %q", got.Sections, want)
	}

	// read at the request, not at the start
	write("curl two")
	decode(t, get(t, h, "/api/help"), &got)
	if len(got.Sections) != 2 || !strings.HasPrefix(got.Sections[0].Body, "curl two") {
		t.Errorf("/api/help after a README change: %q; want the new Install text", got.Sections)
	}

	// a README without the sections, or none: 500 naming why
	if err := os.WriteFile(readme, []byte("# obor\n\n## Install\n\nx\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if r := get(t, h, "/api/help"); r.Code != http.StatusInternalServerError ||
		!strings.Contains(r.Body.String(), "Set up a project") {
		t.Errorf("/api/help, README without Set up a project: %d %s; want 500 naming it", r.Code, r.Body.String())
	}
	if r := get(t, (&Server{Kit: t.TempDir()}).Handler(), "/api/help"); r.Code != http.StatusInternalServerError ||
		!strings.Contains(r.Body.String(), "README.md") {
		t.Errorf("/api/help, no README: %d %s; want 500 naming README.md", r.Code, r.Body.String())
	}
}

// The kit's own README has both sections (the page shows them; a renamed heading fails here, not on the page).
func TestKitReadmeHasHelpSections(t *testing.T) {
	t.Parallel()
	b, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := help.Sections(string(b))
	if err != nil || len(s) != 2 || !strings.Contains(s[0].Body, "install.sh") || !strings.Contains(s[1].Body, "obor init") {
		t.Errorf("README sections: %q, %v", s, err)
	}
}
