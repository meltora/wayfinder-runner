package config

import (
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
)

const (
	CheckInstallEcosystemNPM            = "npm"
	MaxCheckInstallDirectories          = 16
	DefaultCheckInstallTimeoutSeconds   = 600
	MaxCheckInstallTimeoutSeconds       = 1800
	maxCheckInstallDirectoryBytes       = 512
	defaultCheckInstallDirectory        = "."
	checkInstallNodeModulesComponent    = "node_modules"
	checkInstallGitMetadataComponent    = ".git"
	removedDependencyDirectoriesMessage = "dependency_directories is no longer supported; the runner never carries agent directories into checks. Configure install to install dependencies from the published lockfile"
)

type CheckInstallConfig struct {
	Ecosystem      string   `yaml:"ecosystem"`
	Directories    []string `yaml:"directories,omitempty"`
	TimeoutSeconds int      `yaml:"timeout_seconds,omitempty"`
}

func (c *CheckInstallConfig) InstallDirectories() []string {
	if c.Directories == nil {
		return []string{defaultCheckInstallDirectory}
	}
	return append([]string(nil), c.Directories...)
}

func (c *CheckInstallConfig) InstallTimeoutSeconds() int {
	if c.TimeoutSeconds == 0 {
		return DefaultCheckInstallTimeoutSeconds
	}
	return c.TimeoutSeconds
}

func (c *CheckInstallConfig) clone() *CheckInstallConfig {
	if c == nil {
		return nil
	}
	out := *c
	if c.Directories != nil {
		out.Directories = append([]string{}, c.Directories...)
	}
	return &out
}

func validateCheckDependencySettings(check CheckConfig) error {
	if len(check.DependencyDirectories) != 0 {
		return errors.New(removedDependencyDirectoriesMessage)
	}
	if check.Install == nil {
		return nil
	}
	return check.Install.validate()
}

func (c *CheckInstallConfig) validate() error {
	if c.Ecosystem != CheckInstallEcosystemNPM {
		return fmt.Errorf("install ecosystem %q is not supported; supported: %s", c.Ecosystem, CheckInstallEcosystemNPM)
	}
	if c.Directories != nil && (len(c.Directories) == 0 || len(c.Directories) > MaxCheckInstallDirectories) {
		return fmt.Errorf("install directories must contain between 1 and %d entries", MaxCheckInstallDirectories)
	}
	seen := make(map[string]bool, len(c.Directories))
	for _, directory := range c.Directories {
		if !validCheckInstallDirectory(directory) {
			return fmt.Errorf("install directory %q must be a clean repository-relative path without .., .git or node_modules components", directory)
		}
		if seen[directory] {
			return fmt.Errorf("install directory %q is listed more than once", directory)
		}
		seen[directory] = true
	}
	if c.TimeoutSeconds < 0 || c.TimeoutSeconds > MaxCheckInstallTimeoutSeconds {
		return fmt.Errorf("install timeout_seconds must be between 1 and %d", MaxCheckInstallTimeoutSeconds)
	}
	return nil
}

func validCheckInstallDirectory(directory string) bool {
	if directory == defaultCheckInstallDirectory {
		return true
	}
	if directory == "" || len(directory) > maxCheckInstallDirectoryBytes || path.Clean(directory) != directory ||
		strings.HasPrefix(directory, "/") || strings.ContainsAny(directory, "\\") {
		return false
	}
	if strings.ContainsFunc(directory, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return false
	}
	return !slices.ContainsFunc(strings.Split(directory, "/"), func(component string) bool {
		return component == "" || component == "." || component == ".." ||
			strings.EqualFold(component, checkInstallGitMetadataComponent) || component == checkInstallNodeModulesComponent
	})
}
