package daemon

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"sort"
)

type dependencyCopy struct {
	ctx        context.Context
	tree       *copyTarget
	entryLimit int
	byteLimit  int64
	depthLimit int
	entries    int
	bytes      int64
}

func (w *checkWorkspace) syncDependencies(ctx context.Context, root *os.Root, workDir string, dependencies []checkDependency) error {
	previous := w.copied
	w.copied = nil
	if len(dependencies) == 0 {
		return nil
	}
	source, err := os.OpenRoot(workDir)
	if err != nil {
		return err
	}
	defer func() { _ = source.Close() }()
	copied := &dependencyCopy{
		ctx:        ctx,
		tree:       &copyTarget{root: root, racyWindow: candidateRacyWindow, previous: previous},
		entryLimit: w.entryLimit,
		byteLimit:  w.byteLimit,
		depthLimit: w.depthLimit,
	}
	for _, dependency := range dependencies {
		if err := copied.dependency(source, dependency); err != nil {
			return err
		}
	}
	w.copied = copied.tree.next
	return nil
}

func (c *dependencyCopy) dependency(source *os.Root, dependency checkDependency) error {
	info, err := source.Lstat(dependency.source)
	if err != nil || !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
		return errCopySourceChanged
	}
	dir, err := source.OpenRoot(dependency.source)
	if err != nil {
		return errCopySourceChanged
	}
	defer func() { _ = dir.Close() }()
	if !sameDirectory(dir, info) {
		return errCopySourceChanged
	}
	if err := c.tree.root.MkdirAll(dependency.target, 0o755); err != nil {
		return err
	}
	return c.directory(dir, dependency.target, 1)
}

func (c *dependencyCopy) directory(dir *os.Root, rel string, depth int) error {
	if depth > c.depthLimit {
		return checkDependencyLimit("are more than %d directories deep", c.depthLimit)
	}
	handle, err := dir.Open(".")
	if err != nil {
		return errCopySourceChanged
	}
	names, err := handle.Readdirnames(-1)
	closeErr := handle.Close()
	if err := errors.Join(err, closeErr); err != nil {
		return errCopySourceChanged
	}
	sort.Strings(names)
	keep := make(map[string]bool, len(names))
	for _, name := range names {
		keep[name] = true
	}
	if err := c.tree.removeExcept(rel, keep); err != nil {
		return err
	}
	for _, name := range names {
		if err := c.entry(dir, rel, name, depth); err != nil {
			return err
		}
	}
	return nil
}

func (c *dependencyCopy) entry(dir *os.Root, rel, name string, depth int) error {
	if err := c.ctx.Err(); err != nil {
		return err
	}
	c.entries++
	if c.entries > c.entryLimit {
		return checkDependencyLimit("have more than %d entries", c.entryLimit)
	}
	child := joinRel(rel, name)
	info, err := dir.Lstat(name)
	if err != nil {
		return errCopySourceChanged
	}
	if err := c.tree.clearDifferentKind(child, info.Mode()); err != nil {
		return err
	}
	switch mode := info.Mode(); {
	case mode&fs.ModeSymlink != 0:
		destination, err := dir.Readlink(name)
		if err != nil {
			return errCopySourceChanged
		}
		if err := c.count(int64(len(destination))); err != nil {
			return err
		}
		return c.tree.placeSymlink(child, destination)
	case mode.IsDir():
		if err := c.tree.root.Mkdir(child, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
		sub, err := dir.OpenRoot(name)
		if err != nil {
			return errCopySourceChanged
		}
		defer func() { _ = sub.Close() }()
		if !sameDirectory(sub, info) {
			return errCopySourceChanged
		}
		return c.directory(sub, child, depth+1)
	case mode.IsRegular():
		if c.tree.reusable(child, info) {
			return c.count(info.Size())
		}
		written, err := c.tree.copyRegular(dir, child, name, info, c.byteLimit-c.bytes)
		if err != nil {
			return err
		}
		return c.count(written)
	default:
		return c.tree.root.RemoveAll(child)
	}
}

func (c *dependencyCopy) count(size int64) error {
	c.bytes += size
	if c.bytes > c.byteLimit {
		return checkDependencyLimit("have more than %d bytes", c.byteLimit)
	}
	return nil
}
