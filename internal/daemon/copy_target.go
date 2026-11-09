package daemon

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"syscall"
	"time"
)

var (
	errCopySourceChanged    = errors.New("copy source changed while it was read")
	errCopySourceUnreadable = errors.New("copy source cannot be read by the runner")
)

type copyTarget struct {
	root       *os.Root
	racyWindow time.Duration
	previous   map[string]copiedFile
	next       map[string]copiedFile
}

func (t *copyTarget) placeSymlink(child, destination string) error {
	existing, err := t.root.Readlink(child)
	if err == nil && existing == destination {
		return nil
	}
	if err == nil {
		if err := t.root.Remove(child); err != nil {
			return err
		}
	}
	return t.root.Symlink(destination, child)
}

func (t *copyTarget) clearDifferentKind(child string, mode fs.FileMode) error {
	current, err := t.root.Lstat(child)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if current.Mode().Type() == mode.Type() {
		return nil
	}
	return t.root.RemoveAll(child)
}

func (t *copyTarget) removeExcept(rel string, keep map[string]bool) error {
	handle, err := t.root.Open(rel)
	if err != nil {
		return err
	}
	existing, err := handle.Readdirnames(-1)
	closeErr := handle.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	for _, name := range existing {
		if !keep[name] {
			if err := t.root.RemoveAll(joinRel(rel, name)); err != nil {
				return err
			}
		}
	}
	return nil
}

func (t *copyTarget) reusable(child string, info fs.FileInfo) bool {
	recorded, ok := t.previous[child]
	if !ok {
		return false
	}
	source, ok := fileStatKey(info)
	if !ok || source != recorded.source {
		return false
	}
	current, err := t.root.Lstat(child)
	if err != nil || !current.Mode().IsRegular() {
		return false
	}
	target, ok := fileStatKey(current)
	if !ok || target != recorded.target {
		return false
	}
	t.remember(child, recorded)
	return true
}

func (t *copyTarget) remember(child string, copied copiedFile) {
	if t.next == nil {
		t.next = map[string]copiedFile{}
	}
	t.next[child] = copied
}

func (t *copyTarget) copyRegular(dir *os.Root, child, name string, info fs.FileInfo, remaining int64) (int64, error) {
	if err := t.root.RemoveAll(child); err != nil {
		return 0, err
	}
	source, err := dir.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, fs.ErrPermission) {
		return 0, errCopySourceUnreadable
	}
	if err != nil {
		return 0, errCopySourceChanged
	}
	defer func() { _ = source.Close() }()
	opened, err := source.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return 0, errCopySourceChanged
	}
	started := time.Now()
	mode := fs.FileMode(0o644)
	if info.Mode().Perm()&0o111 != 0 {
		mode = 0o755
	}
	target, err := t.root.OpenFile(child, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, mode)
	if err != nil {
		return 0, err
	}
	written, copyErr := io.Copy(target, io.LimitReader(source, remaining+1))
	closeErr := target.Close()
	if written > remaining {
		return written, nil
	}
	if copyErr != nil {
		return written, errCopySourceChanged
	}
	if closeErr != nil {
		return written, closeErr
	}
	if err := t.root.Chmod(child, mode); err != nil {
		return written, err
	}
	t.rememberCopy(child, source, opened, started)
	return written, nil
}

func (t *copyTarget) rememberCopy(child string, source *os.File, before fs.FileInfo, started time.Time) {
	after, err := source.Stat()
	if err != nil {
		return
	}
	sourceKey, ok := fileStatKey(before)
	if afterKey, afterOK := fileStatKey(after); !ok || !afterOK || afterKey != sourceKey {
		return
	}
	if !sourceKey.settledBefore(started, t.racyWindow) {
		return
	}
	copied, err := t.root.Lstat(child)
	if err != nil {
		return
	}
	targetKey, ok := fileStatKey(copied)
	if !ok {
		return
	}
	t.remember(child, copiedFile{source: sourceKey, target: targetKey})
}
