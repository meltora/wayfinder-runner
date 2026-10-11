package plancommand

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const (
	MaxCommandBytes   = 8 << 10
	MaxHookInputBytes = 1 << 20
)

type Guard struct {
	roots []string
}

func New(roots []string) (*Guard, error) {
	if len(roots) == 0 {
		return nil, fmt.Errorf("at least one root is required")
	}
	resolved := make([]string, 0, len(roots))
	for _, root := range roots {
		if strings.ContainsAny(root, "\x00\r\n") || !filepath.IsAbs(root) {
			return nil, fmt.Errorf("root %q must be an absolute path", root)
		}
		real, err := filepath.EvalSymlinks(root)
		if err != nil {
			return nil, fmt.Errorf("root %q is unreadable: %w", root, err)
		}
		info, err := os.Stat(real)
		if err != nil || !info.IsDir() {
			return nil, fmt.Errorf("root %q is not a readable directory", root)
		}
		resolved = append(resolved, real)
	}
	return &Guard{roots: resolved}, nil
}

func (g *Guard) CheckHookInput(raw []byte) error {
	if len(raw) > MaxHookInputBytes {
		return fmt.Errorf("hook input is too large")
	}
	cwd, command, err := decodeHookInput(raw)
	if err != nil {
		return err
	}
	return g.CheckCommand(cwd, command)
}

func (g *Guard) CheckCommand(cwd, command string) error {
	if len(command) > MaxCommandBytes {
		return fmt.Errorf("command exceeds %d bytes", MaxCommandBytes)
	}
	realCwd, err := g.confinedWorkingDirectory(cwd)
	if err != nil {
		return err
	}
	stages, err := splitPipeline(command)
	if err != nil {
		return err
	}
	for _, words := range stages {
		paths, err := checkProgram(words)
		if err != nil {
			return err
		}
		for _, value := range paths {
			if err := g.checkArgumentPath(realCwd, value); err != nil {
				return err
			}
		}
	}
	return nil
}

func (g *Guard) confinedWorkingDirectory(cwd string) (string, error) {
	if cwd == "" || !filepath.IsAbs(cwd) {
		return "", fmt.Errorf("working directory must be an absolute path inside the planning workspace")
	}
	real, err := filepath.EvalSymlinks(cwd)
	if err != nil || !g.contains(real) {
		return "", fmt.Errorf("working directory is outside the planning workspace")
	}
	return real, nil
}

func (g *Guard) checkArgumentPath(cwd, value string) error {
	if value == "" {
		return nil
	}
	if filepath.IsAbs(value) || strings.HasPrefix(value, "~") {
		return fmt.Errorf("argument %q leaves the planning workspace", value)
	}
	for _, segment := range strings.FieldsFunc(value, func(r rune) bool { return r == '/' || r == ':' || r == '=' }) {
		if segment == ".." {
			return fmt.Errorf("argument %q leaves the planning workspace", value)
		}
	}
	return g.checkExistingPath(filepath.Join(cwd, value))
}

func (g *Guard) checkExistingPath(path string) error {
	if _, err := os.Lstat(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("argument path could not be checked")
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil || !g.contains(real) {
		return fmt.Errorf("argument %q resolves outside the planning workspace", filepath.Base(path))
	}
	return nil
}

func (g *Guard) contains(path string) bool {
	for _, root := range g.roots {
		relative, err := filepath.Rel(root, path)
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative) {
			return true
		}
	}
	return false
}
