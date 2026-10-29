package supervisor

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

var errStdinSourceConflict = errors.New("process stdin and stdin file are mutually exclusive")

func validateStdinSource(spec Spec) error {
	if spec.StdinFile == "" {
		return nil
	}
	if spec.Stdin != nil {
		return errStdinSourceConflict
	}
	if !filepath.IsAbs(spec.StdinFile) {
		return fmt.Errorf("process stdin file must be an absolute path")
	}
	return nil
}

func openStdinFile(path string) (*os.File, error) {
	file, err := os.OpenFile(filepath.Clean(path), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open process stdin file: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("inspect process stdin file: %w", err)
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, fmt.Errorf("process stdin file is not a regular file")
	}
	return file, nil
}

func stdinRedirect(spec Spec) string {
	if spec.StdinFile == "" {
		return ""
	}
	return " < " + shellQuote(spec.StdinFile)
}
