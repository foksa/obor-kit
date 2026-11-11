package view

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"obor/internal/checks"
)

// Doc is one markdown file of a project's docs page: its path from the root (slash-separated) and its newest commit's
// time (epoch seconds; 0: staged, not committed yet).
type Doc struct {
	Path    string `json:"path"`
	Updated int64  `json:"updated,omitempty"`
}

// DocGroup is one directory's docs ("" the root), newest commit first.
type DocGroup struct {
	Dir  string `json:"dir"`
	Docs []Doc  `json:"docs"`
}

// DocList is a project's docs: the markdown files git's index tracks under its root at Head, the pinned ones first (in
// PinnedDocs' order) and the rest by directory (the root first, then by name). Nothing else is ever served.
type DocList struct {
	Head   string     `json:"head"`
	Pinned []Doc      `json:"pinned"`
	Groups []DocGroup `json:"groups"`
}

// PinnedDocs are the docs the page lists first, when tracked: the project's BACKLOG.md (wherever it is: the shallowest
// one, the root's first; another, a template's, stays in its directory), then these paths.
var PinnedDocs = []string{"docs/decisions.md", "docs/accepted.md", "AGENTS.md", "README.md"}

// backlog is the pinned file name tracked wherever it is.
const backlog = "BACKLOG.md"

// BuildDocs is the doc list of the tracked files at head.
func BuildDocs(head string, files []checks.TrackedDoc) DocList {
	l := DocList{Head: head, Pinned: []Doc{}, Groups: []DocGroup{}}
	var backlogs []Doc
	pinned := map[string]*Doc{}
	byDir := map[string][]Doc{}
	for _, f := range files {
		d := Doc{Path: f.Path, Updated: f.Updated}
		switch {
		case path.Base(f.Path) == backlog:
			backlogs = append(backlogs, d)
		case slices.Contains(PinnedDocs, f.Path):
			pinned[f.Path] = &d
		default:
			dir := path.Dir(f.Path)
			if dir == "." {
				dir = ""
			}
			byDir[dir] = append(byDir[dir], d)
		}
	}
	slices.SortFunc(backlogs, func(a, b Doc) int { // the root's first, then the shallower, then by path
		return cmp.Or(cmp.Compare(strings.Count(a.Path, "/"), strings.Count(b.Path, "/")), cmp.Compare(a.Path, b.Path))
	})
	if len(backlogs) > 0 {
		l.Pinned = append(l.Pinned, backlogs[0])
		for _, d := range backlogs[1:] {
			dir := path.Dir(d.Path)
			byDir[dir] = append(byDir[dir], d)
		}
	}
	for _, p := range PinnedDocs {
		if d := pinned[p]; d != nil {
			l.Pinned = append(l.Pinned, *d)
		}
	}
	dirs := make([]string, 0, len(byDir))
	for dir := range byDir {
		dirs = append(dirs, dir)
	}
	slices.Sort(dirs) // "" (the root) sorts first
	for _, dir := range dirs {
		docs := byDir[dir]
		slices.SortFunc(docs, func(a, b Doc) int { return cmp.Or(cmp.Compare(b.Updated, a.Updated), cmp.Compare(a.Path, b.Path)) })
		l.Groups = append(l.Groups, DocGroup{Dir: dir, Docs: docs})
	}
	return l
}

// Has is whether p is one of the list's docs, exactly as listed.
func (l DocList) Has(p string) bool {
	has := func(d Doc) bool { return d.Path == p }
	if slices.ContainsFunc(l.Pinned, has) {
		return true
	}
	for _, g := range l.Groups {
		if slices.ContainsFunc(g.Docs, has) {
			return true
		}
	}
	return false
}

// DocCache is the doc list of each project root, read again when the root's HEAD or git index moves (one rev-parse and
// one stat per request, the ls-files and log only when either moved). The zero value reads real git (checks.DocsHead,
// checks.TrackedDocs, checks.DocsIndex, checks.IndexFile); a test injects Head, Files, Index and Tracked.
type DocCache struct {
	Head  func(ctx context.Context, root string) (string, error)
	Files func(ctx context.Context, root string) ([]checks.TrackedDoc, error)
	// Index is the key of root's git index (checks.DocsIndex): the list is read again when it moves.
	Index func(root string) (mtime, size int64, err error)
	// Tracked is whether the index tracks a path as a regular file (checks.IndexFile): a doc is read only while it does.
	Tracked func(ctx context.Context, root, p string) (bool, error)

	mu sync.Mutex
	m  map[string]docItem // by root
}

// docItem is one root's list and the HEAD and index key it was read at.
type docItem struct {
	list        DocList
	mtime, size int64
}

// List is root's doc list at its HEAD and index.
func (c *DocCache) List(ctx context.Context, root string) (DocList, error) {
	head, files, index := c.Head, c.Files, c.Index
	if head == nil {
		head = checks.DocsHead
	}
	if files == nil {
		files = checks.TrackedDocs
	}
	if index == nil {
		index = checks.DocsIndex
	}
	h, err := head(ctx, root)
	if err != nil {
		return DocList{}, fmt.Errorf("%s: HEAD: %w", root, err)
	}
	if h == "" {
		return DocList{}, fmt.Errorf("%s: git names no HEAD", root)
	}
	mtime, size, err := index(root)
	if err != nil {
		return DocList{}, fmt.Errorf("%s: index: %w", root, err)
	}
	c.mu.Lock()
	it, ok := c.m[root]
	c.mu.Unlock()
	if ok && it.list.Head == h && it.mtime == mtime && it.size == size {
		return it.list, nil
	}
	fs, err := files(ctx, root)
	if err != nil {
		return DocList{}, fmt.Errorf("%s: tracked docs: %w", root, err)
	}
	l := BuildDocs(h, fs)
	c.mu.Lock()
	if c.m == nil {
		c.m = map[string]docItem{}
	}
	c.m[root] = docItem{list: l, mtime: mtime, size: size}
	c.mu.Unlock()
	return l, nil
}

// MaxDoc is the largest doc ReadDoc returns.
const MaxDoc = 4 << 20

// ErrNotADoc is a path ReadDoc refuses: git's index does not track it as a regular file under the root, or the list does
// not have it.
var ErrNotADoc = errors.New("not a tracked markdown file")

// ReadDoc is the text of p, one of l's docs, from the checkout at root. p is served only while git's index tracks it as
// a regular file (Tracked at this read), so a path that is no longer tracked, a tracked path replaced on disk by a
// symlink, a path not in the list (a "../" never is) and one whose real path leaves the root are ErrNotADoc.
func (c *DocCache) ReadDoc(ctx context.Context, root string, l DocList, p string) (string, error) {
	if !l.Has(p) {
		return "", fmt.Errorf("%w: %s", ErrNotADoc, p)
	}
	tracked := c.Tracked
	if tracked == nil {
		tracked = checks.IndexFile
	}
	ok, err := tracked(ctx, root, p)
	if err != nil {
		return "", fmt.Errorf("%s: index: %w", root, err)
	}
	if !ok {
		return "", fmt.Errorf("%w: %s is not in git's index", ErrNotADoc, p)
	}
	onDisk := filepath.Join(root, filepath.FromSlash(p))
	if fi, err := os.Lstat(onDisk); err != nil {
		return "", err
	} else if fi.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("%w: %s is a symlink on disk", ErrNotADoc, p)
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(onDisk)
	if err != nil {
		return "", err
	}
	if rel, err := filepath.Rel(realRoot, real); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %s leaves the project's root", ErrNotADoc, p)
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > MaxDoc {
		return "", fmt.Errorf("%s: not a regular file of at most %d bytes", p, MaxDoc)
	}
	b, err := os.ReadFile(real)
	return string(b), err
}
