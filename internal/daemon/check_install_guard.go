package daemon

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path"
	"sort"
)

const (
	maxGuardEntries = 4_000_000
	maxGuardDepth   = 1024
)

type installGuard struct {
	ctx        context.Context
	root       *os.Root
	checkID    string
	entryLimit int
	depthLimit int
}

type guardEntry struct {
	kind       string
	executable bool
	size       int64
	digest     string
}

type guardSnapshot map[string]guardEntry

func (w *checkWorkspace) newInstallGuard(ctx context.Context, root *os.Root, checkID string) *installGuard {
	return &installGuard{ctx: ctx, root: root, checkID: checkID, entryLimit: w.guardEntryLimit, depthLimit: maxGuardDepth}
}

func (g *installGuard) snapshot() (guardSnapshot, error) {
	snapshot := guardSnapshot{}
	err := g.walk(nil, func(rel string, info fs.FileInfo) error {
		entry, err := g.describe(rel, info, true)
		snapshot[rel] = entry
		return err
	})
	return snapshot, err
}

func (g *installGuard) changed(before guardSnapshot, skip map[string]bool) (string, bool, error) {
	var changes []string
	seen := map[string]bool{}
	err := g.walk(skip, func(rel string, info fs.FileInfo) error {
		seen[rel] = true
		previous, existed := before[rel]
		if !existed || previous.kind != guardKind(info) {
			changes = append(changes, rel)
			return nil
		}
		hash := previous.kind == "file" && previous.size == info.Size() && previous.executable == executableByOwner(info)
		current, err := g.describe(rel, info, hash)
		if err != nil {
			return err
		}
		if current != previous {
			changes = append(changes, rel)
		}
		return nil
	})
	if err != nil {
		return "", false, err
	}
	for rel := range before {
		if !seen[rel] {
			changes = append(changes, rel)
		}
	}
	if len(changes) == 0 {
		return "", false, nil
	}
	sort.Strings(changes)
	return changes[0], true, nil
}

func (g *installGuard) walk(skip map[string]bool, visit func(rel string, info fs.FileInfo) error) error {
	entries := 0
	var directory func(rel string, depth int) error
	directory = func(rel string, depth int) error {
		if depth > g.depthLimit {
			return g.limit("is more than %d directories deep", g.depthLimit)
		}
		handle, err := g.root.Open(rel)
		if err != nil {
			return err
		}
		names, err := handle.Readdirnames(-1)
		if closeErr := handle.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return err
		}
		sort.Strings(names)
		for _, name := range names {
			if err := g.ctx.Err(); err != nil {
				return err
			}
			child := path.Join(rel, name)
			if skip[child] {
				continue
			}
			entries++
			if entries > g.entryLimit {
				return g.limit("has more than %d entries", g.entryLimit)
			}
			info, err := g.root.Lstat(child)
			if err != nil {
				return err
			}
			if err := visit(child, info); err != nil {
				return err
			}
			if info.IsDir() {
				if err := directory(child, depth+1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return directory(".", 1)
}

func (g *installGuard) describe(rel string, info fs.FileInfo, hash bool) (guardEntry, error) {
	entry := guardEntry{kind: guardKind(info)}
	switch entry.kind {
	case "symlink":
		target, err := g.root.Readlink(rel)
		if err != nil {
			return entry, err
		}
		entry.digest = sha256Hex([]byte(target))
	case "file":
		entry.executable, entry.size = executableByOwner(info), info.Size()
		if hash {
			digest, err := (&installedTreeScan{root: g.root}).fileDigest(rel, info.Size())
			if err != nil {
				return entry, err
			}
			entry.digest = digest
		}
	}
	return entry, nil
}

func guardKind(info fs.FileInfo) string {
	switch mode := info.Mode(); {
	case mode&fs.ModeSymlink != 0:
		return "symlink"
	case mode.IsDir():
		return "dir"
	case mode.IsRegular():
		return "file"
	default:
		return "special"
	}
}

func executableByOwner(info fs.FileInfo) bool {
	return info.Mode().Perm()&0o100 != 0
}

func (g *installGuard) limit(format string, args ...any) error {
	return &installRefusal{message: "dependency install for check " + g.checkID + " could not compare the check workspace: it " + fmt.Sprintf(format, args...)}
}
