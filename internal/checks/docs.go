package checks

import (
	"context"
	"strconv"
	"strings"

	"obor/internal/project"
)

// TrackedDoc is one markdown file git tracks under a project's root: its path (relative to the root, slash-separated)
// and its newest commit's time (unix seconds; 0: only staged, no commit has it yet).
type TrackedDoc struct {
	Path    string
	Updated int64
}

// docsSpec is the pathspec of the docs: every .md file, in every directory.
const docsSpec = "*.md"

// DocsHead is HEAD's sha in the repository at root: the key a doc list is cached by.
func DocsHead(ctx context.Context, root string) (string, error) {
	out, err := project.GitCtx(ctx, root, "rev-parse", "HEAD")
	return strings.TrimSpace(out), err
}

// TrackedDocs is the markdown files git tracks under root (ls-files: untracked and ignored files are not in it;
// symlinks and submodules are left out, so a served path is always a regular file git has), each with the time of the
// newest commit that touched it (one log over the same pathspec), in ls-files order.
func TrackedDocs(ctx context.Context, root string) ([]TrackedDoc, error) {
	ls, err := project.GitCtx(ctx, root, "ls-files", "-s", "-z", "--", docsSpec)
	if err != nil {
		return nil, err
	}
	var docs []TrackedDoc
	seen := map[string]int{}
	for _, entry := range strings.Split(ls, "\x00") {
		meta, path, ok := strings.Cut(entry, "\t")
		if !ok || !strings.HasPrefix(meta, "100") { // 100644 or 100755: a regular file (120000 a symlink, 160000 a submodule)
			continue
		}
		if _, dup := seen[path]; dup { // a conflicted path is listed once per stage
			continue
		}
		seen[path] = len(docs)
		docs = append(docs, TrackedDoc{Path: path})
	}
	if len(docs) == 0 {
		return docs, nil
	}
	log, err := project.GitCtx(ctx, root, "log", "-z", "--format=%x01%ct", "--name-only", "--relative", "--", docsSpec)
	if err != nil {
		return nil, err
	}
	var at int64
	for _, tok := range strings.Split(log, "\x00") {
		if t, ok := strings.CutPrefix(tok, "\x01"); ok {
			at, _ = strconv.ParseInt(t, 10, 64)
			continue
		}
		i, ok := seen[strings.TrimPrefix(tok, "\n")]
		if ok && docs[i].Updated == 0 { // log is newest first: the first commit naming a path is its newest
			docs[i].Updated = at
		}
	}
	return docs, nil
}
