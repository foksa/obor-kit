package rex

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// obor setup check on Rex: the CLI found on the PATH of the client's environment is its line; none on PATH is the
// refusal naming the app bundle's directory, and every call fails with the same refusal before it runs anything (no
// path is guessed: a rex inside the bundle is never run). A Bin given is the CLI, PATH unread.
func TestCheckFindsRexOnPath(t *testing.T) {
	t.Parallel()
	empty, withRex := t.TempDir(), t.TempDir()
	bin := filepath.Join(withRex, "rex")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	line, err := (&Client{Env: []string{"PATH=" + empty + ":" + withRex}}).Check(ctx)
	if err != nil || line != "rex CLI "+bin {
		t.Errorf("rex on PATH: line %q, error %v; want %q", line, err, "rex CLI "+bin)
	}
	const refusal = "rex check: no rex on PATH: Rex's CLI ships in its app bundle, put " +
		"/Applications/Rex Beta.app/Contents/Helpers/Rex Server.rex/Contents/MacOS on PATH"
	none := &Client{Env: []string{"PATH=" + empty}}
	if line, err := none.Check(ctx); err == nil || err.Error() != refusal || line != "" {
		t.Errorf("no rex on PATH: line %q, error %v; want the refusal %q", line, err, refusal)
	}
	if err := none.call(ctx, nil, "session.list", nil); err == nil || err.Error() != "rex session.list: no rex on PATH: "+
		"Rex's CLI ships in its app bundle, put "+BundleDir+" on PATH" {
		t.Errorf("a call with no rex on PATH: %v, want the same refusal named by its method", err)
	}
	if line, err := (&Client{Bin: "/opt/rex/rex", Env: []string{"PATH=" + empty}}).Check(ctx); err != nil || line != "rex CLI /opt/rex/rex" {
		t.Errorf("Bin given: line %q, error %v; want it, PATH unread", line, err)
	}
}
