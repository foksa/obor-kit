package checks

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
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

// DocsHead is HEAD's sha in the repository at root: a doc list is cached by it and by DocsIndex.
func DocsHead(ctx context.Context, root string) (string, error) {
	out, err := project.GitCtx(ctx, root, "rev-parse", "HEAD")
	return strings.TrimSpace(out), err
}

// DocsIndex is the key of the repository at root's index: the `.git/index` file's modification time (unix nanoseconds)
// and size. A git command that changes what the index tracks writes that file, so a list cached under the key is read
// again even at the same HEAD.
func DocsIndex(root string) (mtime, size int64, err error) {
	r, err := project.Find(root)
	if err != nil {
		return 0, 0, err
	}
	fi, err := os.Stat(filepath.Join(r.GitDir, "index"))
	if err != nil {
		return 0, 0, err
	}
	return fi.ModTime().UnixNano(), fi.Size(), nil
}

// IndexFile is whether git's index in the repository at root tracks p as a regular file: mode 100644 or 100755, never a
// symlink (120000) or a submodule (160000). One `git ls-files --error-unmatch`, whose exit 1 is a plain no; p is a path
// read from the index, so its name matches literally (a `*` or `[` in it is not a pattern).
func IndexFile(ctx context.Context, root, p string) (bool, error) {
	out, err := project.GitCtx(ctx, root, "--literal-pathspecs", "ls-files", "--error-unmatch", "-s", "-z", "--", p)
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return false, nil
		}
		return false, err
	}
	for _, entry := range strings.Split(out, "\x00") {
		meta, path, ok := strings.Cut(entry, "\t")
		if ok && path == p && strings.HasPrefix(meta, "100") {
			return true, nil
		}
	}
	return false, nil
}

// DocMatch is one line of a tracked doc that a search's words all hold: the doc's path (relative to the root,
// slash-separated), the line's number (1-based) and the line as it is.
type DocMatch struct {
	Path string
	Line int
	Text string
}

// GrepDocs is every line of root's tracked markdown files that holds all of words, case aside, a path's hits in path
// and line order: one `git grep --cached` over the index (the doc set TrackedDocs lists: untracked, ignored and
// worktree-only paths are not searched), the words as fixed strings, one -e each. --all-match keeps the files holding
// all of them; the lines holding only some are dropped here. git's exit 1 is no hit, not an error, and -z keeps a path
// with a colon in it whole.
func GrepDocs(ctx context.Context, root string, words []string) ([]DocMatch, error) {
	if len(words) == 0 {
		return nil, nil
	}
	// --all-match drops the files holding only some of the words; the lines holding only some are holdsAll's.
	args := []string{"grep", "--cached", "--all-match", "-i", "-F", "-n", "-z"}
	for _, w := range words {
		args = append(args, "-e", w)
	}
	out, err := project.GitCtx(ctx, root, append(args, "--", docsSpec)...)
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return nil, nil
		}
		return nil, err
	}
	var ms []DocMatch
	for _, rec := range strings.Split(out, "\n") {
		path, rest, ok := strings.Cut(rec, "\x00")
		if !ok {
			continue // no -z fields: git's own "Binary file … matches"
		}
		number, text, ok := strings.Cut(rest, "\x00")
		if !ok || !holdsAll(text, words) {
			continue
		}
		if n, err := strconv.Atoi(number); err == nil {
			ms = append(ms, DocMatch{Path: path, Line: n, Text: text})
		}
	}
	return ms, nil
}

// holdsAll is whether line holds every one of words, case aside.
func holdsAll(line string, words []string) bool {
	lower := strings.ToLower(line)
	for _, w := range words {
		if !strings.Contains(lower, strings.ToLower(w)) {
			return false
		}
	}
	return true
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
