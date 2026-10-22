package daemon

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
	"syscall"
)

var errWorkspaceSymlink = errors.New("workspace path contains a symbolic link")

func requireWorkspaceDirectories(root *os.Root, rel string) error {
	if rel == "." {
		return nil
	}
	parts := strings.Split(rel, "/")
	for index := range parts {
		prefix := strings.Join(parts[:index+1], "/")
		info, err := root.Lstat(prefix)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: %s", errWorkspaceSymlink, prefix)
		}
		if !info.IsDir() {
			return fmt.Errorf("workspace path component %s is not a directory", prefix)
		}
	}
	return nil
}

func inspectWorkspaceFile(root *os.Root, rel string, limit int64) (fs.FileInfo, error) {
	if err := requireWorkspaceDirectories(root, path.Dir(rel)); err != nil {
		return nil, err
	}
	info, err := root.Lstat(rel)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("workspace file %s is not a regular file", rel)
	}
	if info.Size() > limit {
		return nil, fmt.Errorf("workspace file %s exceeds %d bytes", rel, limit)
	}
	return info, nil
}

func openInspectedWorkspaceFile(root *os.Root, rel string, flag int, expected fs.FileInfo) (*os.File, error) {
	file, err := root.OpenFile(rel, flag|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(expected, opened) {
		_ = file.Close()
		return nil, fmt.Errorf("workspace file %s changed while it was opened", rel)
	}
	return file, nil
}

func readWorkspaceFile(root *os.Root, rel string, limit int64) ([]byte, error) {
	info, err := inspectWorkspaceFile(root, rel, limit)
	if err != nil {
		return nil, err
	}
	file, err := openInspectedWorkspaceFile(root, rel, os.O_RDONLY, info)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	content, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > limit {
		return nil, fmt.Errorf("workspace file %s exceeds %d bytes", rel, limit)
	}
	return content, nil
}

func hasSingleLink(info fs.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && uint64(stat.Nlink) == 1
}

func appendWorkspaceFile(root *os.Root, rel string, existing fs.FileInfo, addition string) error {
	var file *os.File
	var err error
	if existing == nil {
		file, err = root.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o644)
	} else {
		file, err = openInspectedWorkspaceFile(root, rel, os.O_WRONLY|os.O_APPEND, existing)
	}
	if err != nil {
		return err
	}
	if opened, err := file.Stat(); err != nil || !hasSingleLink(opened) {
		_ = file.Close()
		return fmt.Errorf("workspace file %s has more than one link", rel)
	}
	if _, err := file.WriteString(addition); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}
