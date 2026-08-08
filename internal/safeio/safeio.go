package safeio

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ReadRegularFile reads a single path through a directory-scoped handle. It
// rejects symbolic links, non-regular files, forbidden permission bits, and
// oversized content before returning any bytes.
func ReadRegularFile(path string, maxSize int64, forbiddenPermissions os.FileMode) ([]byte, error) {
	if maxSize < 0 || path == "" || strings.ContainsRune(path, '\x00') {
		return nil, fmt.Errorf("invalid file path or size limit")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve file path: %w", err)
	}
	base := filepath.Base(abs)
	if base == "." || base == string(filepath.Separator) {
		return nil, fmt.Errorf("path must identify a file")
	}
	root, err := os.OpenRoot(filepath.Dir(abs))
	if err != nil {
		return nil, fmt.Errorf("open containing directory: %w", err)
	}
	defer func() { _ = root.Close() }()
	info, err := root.Lstat(base)
	if err != nil {
		return nil, fmt.Errorf("inspect file: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&forbiddenPermissions != 0 {
		return nil, fmt.Errorf("file must be a regular non-symlink with permitted access mode")
	}
	if info.Size() < 0 || info.Size() > maxSize {
		return nil, fmt.Errorf("file exceeds size limit")
	}
	file, err := root.Open(base)
	if err != nil {
		return nil, fmt.Errorf("open file: %w", err)
	}
	defer func() { _ = file.Close() }()
	openedInfo, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect opened file: %w", err)
	}
	if !openedInfo.Mode().IsRegular() || openedInfo.Mode().Perm()&forbiddenPermissions != 0 || !os.SameFile(info, openedInfo) {
		return nil, fmt.Errorf("file changed while it was being opened")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxSize+1))
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}
	if int64(len(data)) > maxSize {
		return nil, fmt.Errorf("file exceeds size limit")
	}
	return data, nil
}

// MakeTreeRemovable restores owner access on directories beneath a trusted job
// root without following repository-created symbolic links. Callers must stop
// every process that can modify the tree before invoking it.
func MakeTreeRemovable(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("cleanup root must be a real directory")
	}
	// #nosec G302 -- this is a private cleanup directory; owner traversal requires 0700.
	if err := os.Chmod(path, 0o700); err != nil {
		return fmt.Errorf("secure cleanup root: %w", err)
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return fmt.Errorf("open cleanup root: %w", err)
	}
	defer func() { _ = root.Close() }()
	return fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if entry.IsDir() {
			if err := root.Chmod(name, 0o700); err != nil {
				return fmt.Errorf("make cleanup directory %q accessible: %w", name, err)
			}
		}
		return nil
	})
}

// SyncDirectory durably commits directory-entry changes through a scoped
// directory handle.
func SyncDirectory(path string) error {
	root, err := os.OpenRoot(path)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}

// AtomicWriteFile commits a file beneath root by syncing a same-directory
// temporary file and renaming it into place.
func AtomicWriteFile(root, relative string, data []byte, perm os.FileMode) error {
	clean, err := cleanRelative(relative)
	if err != nil {
		return err
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return fmt.Errorf("open root: %w", err)
	}
	defer func() { _ = r.Close() }()
	parent := filepath.Dir(clean)
	if parent != "." {
		if err := r.MkdirAll(parent, 0o700); err != nil {
			return err
		}
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return err
	}
	tmpName := filepath.Join(parent, ".tmp-"+hex.EncodeToString(random[:]))
	tmp, err := r.OpenFile(tmpName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	keep := false
	defer func() {
		_ = tmp.Close()
		if !keep {
			_ = r.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := r.Rename(tmpName, clean); err != nil {
		return err
	}
	keep = true
	dir, err := r.Open(parent)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}

// OpenRegular opens a regular file beneath root without permitting absolute
// paths, traversal, or symlink components. os.Root provides the final
// race-resistant containment boundary even if repository code changes a path
// while it is being inspected.
func OpenRegular(root, relative string) (*os.File, error) {
	clean, err := cleanRelative(relative)
	if err != nil {
		return nil, err
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, fmt.Errorf("open root: %w", err)
	}
	defer func() { _ = r.Close() }()

	parts := strings.Split(filepath.ToSlash(clean), "/")
	for i := range parts {
		prefix := filepath.FromSlash(strings.Join(parts[:i+1], "/"))
		info, err := r.Lstat(prefix)
		if err != nil {
			return nil, fmt.Errorf("inspect %q: %w", relative, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("path %q contains a symbolic link", relative)
		}
		if i < len(parts)-1 && !info.IsDir() {
			return nil, fmt.Errorf("path component %q is not a directory", prefix)
		}
	}

	f, err := r.Open(clean)
	if err != nil {
		return nil, fmt.Errorf("open %q: %w", relative, err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, fmt.Errorf("path %q is not a regular file", relative)
	}
	return f, nil
}

// RemoveRegular removes a regular file beneath root without following symlinks.
// The caller must close any open handle before invoking it.
func RemoveRegular(root, relative string) error {
	clean, err := cleanRelative(relative)
	if err != nil {
		return err
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return fmt.Errorf("open root: %w", err)
	}
	defer func() { _ = r.Close() }()
	info, err := r.Lstat(clean)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("path %q is not a regular file", relative)
	}
	return r.Remove(clean)
}

func cleanRelative(name string) (string, error) {
	if name == "" || filepath.IsAbs(name) || strings.ContainsRune(name, '\x00') || strings.Contains(name, `\`) {
		return "", fmt.Errorf("path must be a non-empty portable relative path")
	}
	clean := filepath.Clean(name)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || !fs.ValidPath(filepath.ToSlash(clean)) {
		return "", fmt.Errorf("path %q escapes its root", name)
	}
	return clean, nil
}
