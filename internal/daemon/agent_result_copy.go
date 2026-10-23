package daemon

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	maxAgentExcludeBytes      = 1 << 20
	maxAgentResultDepth       = 256
	maxAgentResultPathBytes   = maxWorktreePointerBytes - 1
	agentResultResolveTimeout = 10 * time.Minute
	maxQuotedEntryBytes       = 256
)

type agentSnapshot struct {
	candidate     agentCandidate
	skip          map[string]bool
	excludeRel    string
	commonGitRel  string
	start         string
	repositoryID  string
	workspaceRoot string
}

type candidateCopy struct {
	ctx          context.Context
	trusted      *trustedResultRepository
	target       *os.Root
	tree         *copyTarget
	oracle       *ignoreOracle
	skip         map[string]bool
	repositoryID string
	label        string
	entries      int
	bytes        int64
	ignoredDirs  []string
}

type snapshotOutcome struct {
	tree        string
	ignoredDirs []string
}

func (t *trustedResultRepository) snapshot(ctx context.Context, workspace *os.Root, input agentSnapshot) (snapshotOutcome, error) {
	label := describeCandidate(input.candidate)
	patterns, err := readOptionalWorkspaceFile(workspace, input.excludeRel, maxAgentExcludeBytes)
	if err != nil {
		return snapshotOutcome{}, agentResultRefusal(input.repositoryID, "the checkout's .git/info/exclude is not a regular file within %d bytes", maxAgentExcludeBytes)
	}
	if err := writeTrustedExclude(t.gitDir, string(patterns)); err != nil {
		return snapshotOutcome{}, err
	}
	cache, err := t.candidateCache(input.candidate.relPath)
	if err != nil {
		return snapshotOutcome{}, err
	}
	outcome, err := t.snapshotInto(ctx, workspace, input, label, cache)
	if err != nil {
		return snapshotOutcome{}, errors.Join(err, t.dropCandidateCache(input.candidate.relPath))
	}
	return outcome, nil
}

func (t *trustedResultRepository) snapshotInto(ctx context.Context, workspace *os.Root, input agentSnapshot, label string, cache *candidateCache) (snapshotOutcome, error) {
	if _, err := t.gitAt(ctx, t.indexedGit(cache), "read-tree", "-m", input.start); err != nil {
		return snapshotOutcome{}, err
	}
	copied, err := t.copyCandidate(ctx, workspace, input, label, cache)
	if err != nil {
		return snapshotOutcome{}, err
	}
	if _, err := t.gitAt(ctx, t.indexedGit(cache), "add", "-A", "--", ".", runnerArtifactsPathspec); err != nil {
		return snapshotOutcome{}, err
	}
	if err := t.recordSubmodulePointers(ctx, workspace, cache, input, label); err != nil {
		return snapshotOutcome{}, err
	}
	if sparseCheckoutEnabled(workspace, input.commonGitRel, input.candidate.adminRel) {
		sparseRel := path.Join(input.candidate.adminRel, "info", "sparse-checkout")
		if err := t.restoreSparseDeletions(ctx, workspace, cache, sparseRel, input.start, input.repositoryID, label); err != nil {
			return snapshotOutcome{}, err
		}
	}
	tree, err := t.gitAt(ctx, t.indexedGit(cache), "write-tree")
	if err != nil {
		return snapshotOutcome{}, err
	}
	cache.copied = copied.out().next
	return snapshotOutcome{tree: strings.TrimSpace(string(tree)), ignoredDirs: copied.ignoredDirs}, nil
}

func readOptionalWorkspaceFile(workspace *os.Root, rel string, limit int64) ([]byte, error) {
	content, err := readWorkspaceFile(workspace, rel, limit)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return content, err
}

func (t *trustedResultRepository) copyCandidate(ctx context.Context, workspace *os.Root, input agentSnapshot, label string, cache *candidateCache) (*candidateCopy, error) {
	refuse := func(format string, args ...any) error {
		return agentResultRefusal(input.repositoryID, label+" "+format, args...)
	}
	info, err := workspace.Lstat(input.candidate.relPath)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, refuse("is not a real directory")
	}
	source, err := workspace.OpenRoot(input.candidate.relPath)
	if err != nil {
		return nil, refuse("could not be opened")
	}
	defer func() { _ = source.Close() }()
	if !sameDirectory(source, info) {
		return nil, refuse("changed while it was read")
	}
	target, err := os.OpenRoot(cache.files)
	if err != nil {
		return nil, err
	}
	defer func() { _ = target.Close() }()
	oracle, err := t.startOracle(ctx, t.indexedGit(cache), false)
	if err != nil {
		return nil, err
	}
	copier := &candidateCopy{
		ctx: ctx, trusted: t, target: target, oracle: oracle, skip: input.skip,
		repositoryID: input.repositoryID, label: label,
		tree: &copyTarget{root: target, racyWindow: t.racyWindow, previous: cache.copied, next: map[string]copiedFile{}},
	}
	copyErr := copier.directory(source, ".")
	closeErr := oracle.close()
	if copyErr != nil {
		return nil, copyErr
	}
	return copier, closeErr
}

func (c *candidateCopy) out() *copyTarget {
	if c.tree == nil {
		c.tree = &copyTarget{root: c.target, racyWindow: c.trusted.racyWindow}
	}
	return c.tree
}

func sameDirectory(root *os.Root, expected fs.FileInfo) bool {
	opened, err := root.Stat(".")
	return err == nil && opened.IsDir() && os.SameFile(expected, opened)
}

func (c *candidateCopy) refuse(format string, args ...any) error {
	return agentResultRefusal(c.repositoryID, c.label+" "+format, args...)
}

func (c *candidateCopy) directory(dir *os.Root, rel string) error {
	names, err := c.list(dir, rel)
	if err != nil {
		return err
	}
	var ordered, rest []string
	for _, name := range names {
		if rel == "." && name == ".git" {
			continue
		}
		if strings.EqualFold(name, ".git") {
			return c.refuse("contains a nested Git repository at %s", displayEntry(joinRel(rel, name)))
		}
		if gitLookalikeName(name, ".git", "git~1") {
			return c.refuse("contains a name Git treats as .git at %s; rename or remove it", quotedEntry(joinRel(rel, name)))
		}
		if name == ".gitignore" || name == ".gitattributes" {
			ordered = append(ordered, name)
		} else {
			rest = append(rest, name)
		}
	}
	ordered = append(ordered, rest...)
	keep := make(map[string]bool, len(ordered))
	for _, name := range ordered {
		if !c.skip[joinRel(rel, name)] {
			keep[name] = true
		}
	}
	if err := c.out().removeExcept(rel, keep); err != nil {
		return err
	}
	for _, name := range ordered {
		if err := c.entry(dir, rel, name); err != nil {
			return err
		}
	}
	return nil
}

func (c *candidateCopy) list(dir *os.Root, rel string) ([]string, error) {
	handle, err := dir.Open(".")
	if err != nil {
		return nil, c.refuse("could not read %s", displayEntry(rel))
	}
	defer func() { _ = handle.Close() }()
	var names []string
	for {
		batch, err := handle.ReadDir(1024)
		for _, entry := range batch {
			names = append(names, entry.Name())
		}
		if c.entries+len(names) > c.trusted.entryLimit {
			return nil, c.refuse("has more than %d entries", c.trusted.entryLimit)
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, c.refuse("could not read %s", displayEntry(rel))
		}
	}
	sort.Strings(names)
	return names, nil
}

func (c *candidateCopy) entry(dir *os.Root, rel, name string) error {
	child := joinRel(rel, name)
	c.entries++
	if c.entries > c.trusted.entryLimit {
		return c.refuse("has more than %d entries", c.trusted.entryLimit)
	}
	if c.skip[child] {
		return nil
	}
	if err := c.ctx.Err(); err != nil {
		return err
	}
	if len(child) > c.trusted.pathLimit {
		return c.refuse("has a path longer than %d bytes under %s", c.trusted.pathLimit, displayEntry(topComponent(child)))
	}
	info, err := dir.Lstat(name)
	if err != nil {
		return c.refuse("changed while it was read at %s", displayEntry(child))
	}
	if err := c.out().clearDifferentKind(child, info.Mode()); err != nil {
		return err
	}
	switch mode := info.Mode(); {
	case mode&os.ModeSymlink != 0:
		return c.symlink(dir, child, name)
	case mode.IsDir():
		return c.subdirectory(dir, child, name, info)
	case mode.IsRegular():
		return c.file(dir, child, name, info)
	default:
		ignored, err := c.oracle.ignored(child)
		if err != nil || ignored {
			return err
		}
		return c.refuse("contains a special file %s", displayEntry(child))
	}
}

func (c *candidateCopy) subdirectory(parent *os.Root, child, name string, info fs.FileInfo) error {
	if strings.Count(child, "/")+1 > c.trusted.depthLimit {
		return c.refuse("is more than %d directories deep under %s", c.trusted.depthLimit, displayEntry(topComponent(child)))
	}
	if err := c.target.Mkdir(child, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	if c.trusted.gitlinks[child] {
		return nil
	}
	ignored, err := c.oracle.ignored(child)
	if err != nil {
		return err
	}
	if ignored {
		c.ignoredDirs = append(c.ignoredDirs, child)
		return c.target.RemoveAll(child)
	}
	sub, err := parent.OpenRoot(name)
	if err != nil {
		return c.refuse("changed while it was read at %s", displayEntry(child))
	}
	defer func() { _ = sub.Close() }()
	if !sameDirectory(sub, info) {
		return c.refuse("changed while it was read at %s", displayEntry(child))
	}
	return c.directory(sub, child)
}

func (c *candidateCopy) symlink(dir *os.Root, child, name string) error {
	if gitmodulesLookalike(name) {
		return c.refuse("has a .gitmodules symbolic link Git refuses to store at %s; replace it with a regular file", quotedEntry(child))
	}
	destination, err := dir.Readlink(name)
	if err != nil {
		return c.refuse("changed while it was read at %s", displayEntry(child))
	}
	c.bytes += int64(len(destination))
	if c.bytes > c.trusted.byteLimit {
		return c.refuse("has more than %d bytes of file content", c.trusted.byteLimit)
	}
	if err := c.out().placeSymlink(child, destination); err != nil {
		return err
	}
	ignored, err := c.oracle.ignored(child)
	if err != nil {
		return err
	}
	if ignored {
		return c.target.Remove(child)
	}
	return nil
}

func (c *candidateCopy) file(dir *os.Root, child, name string, info fs.FileInfo) error {
	if name != ".gitignore" && name != ".gitattributes" {
		ignored, err := c.oracle.ignored(child)
		if err != nil {
			return err
		}
		if ignored {
			return c.target.RemoveAll(child)
		}
	}
	remaining := c.trusted.byteLimit - c.bytes
	if info.Size() > remaining {
		return c.refuse("has more than %d bytes of file content", c.trusted.byteLimit)
	}
	if c.out().reusable(child, info) {
		c.bytes += info.Size()
		return nil
	}
	written, err := c.out().copyRegular(dir, child, name, info, remaining)
	c.bytes += written
	if c.bytes > c.trusted.byteLimit {
		return c.refuse("has more than %d bytes of file content", c.trusted.byteLimit)
	}
	if errors.Is(err, errCopySourceChanged) {
		return c.refuse("changed while it was read at %s", displayEntry(child))
	}
	return err
}

func joinRel(rel, name string) string {
	if rel == "." {
		return name
	}
	return rel + "/" + name
}

func displayEntry(rel string) string {
	if safeWorkspacePath(rel) {
		return rel
	}
	return "an entry with an unprintable name"
}

func quotedEntry(rel string) string {
	if len(rel) > maxQuotedEntryBytes {
		rel = strings.ToValidUTF8(rel[:maxQuotedEntryBytes], "") + "..."
	}
	return strconv.QuoteToASCII(rel)
}

func topComponent(rel string) string {
	top, _, _ := strings.Cut(rel, "/")
	return top
}
