package config

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func installCheck(install *CheckInstallConfig) map[string]CheckConfig {
	return map[string]CheckConfig{"web": {Command: "npm", Args: []string{"test"}, Install: install}}
}

func TestCheckConfigurationRefusesUnsupportedDependencyAndInstallSettings(t *testing.T) {
	many := make([]string, 17)
	for index := range many {
		many[index] = "packages/p" + strings.Repeat("x", index+1)
	}
	for _, tc := range []struct {
		name   string
		checks map[string]CheckConfig
		want   string
	}{
		{"carried dependency directories", map[string]CheckConfig{"web": {Command: "npm", DependencyDirectories: []string{"node_modules"}}},
			"check web: dependency_directories is no longer supported; the runner never carries agent directories into checks. Configure install to install dependencies from the published lockfile"},
		{"carried dependency directories with an install", map[string]CheckConfig{"web": {Command: "npm", DependencyDirectories: []string{".venv"}, Install: &CheckInstallConfig{Ecosystem: "npm"}}},
			"check web: dependency_directories is no longer supported; the runner never carries agent directories into checks. Configure install to install dependencies from the published lockfile"},
		{"missing ecosystem", installCheck(&CheckInstallConfig{}), `check web: install ecosystem "" is not supported; supported: npm`},
		{"pnpm", installCheck(&CheckInstallConfig{Ecosystem: "pnpm"}), `check web: install ecosystem "pnpm" is not supported; supported: npm`},
		{"upper-case npm", installCheck(&CheckInstallConfig{Ecosystem: "NPM"}), `check web: install ecosystem "NPM" is not supported; supported: npm`},
		{"empty directory list", installCheck(&CheckInstallConfig{Ecosystem: "npm", Directories: []string{}}), "check web: install directories must contain between 1 and 16 entries"},
		{"too many directories", installCheck(&CheckInstallConfig{Ecosystem: "npm", Directories: many}), "check web: install directories must contain between 1 and 16 entries"},
		{"duplicate directory", installCheck(&CheckInstallConfig{Ecosystem: "npm", Directories: []string{"a", "b", "a"}}), `check web: install directory "a" is listed more than once`},
		{"zero timeout is the default, negative is refused", installCheck(&CheckInstallConfig{Ecosystem: "npm", TimeoutSeconds: -1}), "check web: install timeout_seconds must be between 1 and 1800"},
		{"timeout above the maximum", installCheck(&CheckInstallConfig{Ecosystem: "npm", TimeoutSeconds: 1801}), "check web: install timeout_seconds must be between 1 and 1800"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := productionConfig()
			cfg.Checks = tc.checks
			err := cfg.Validate()
			if err == nil || err.Error() != tc.want {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestCheckInstallDirectoriesMustBeCleanRepositoryRelativePaths(t *testing.T) {
	for _, directory := range []string{
		"", "/abs", "/", "..", "../web", "a/../b", "a/..", "./a", "a/", "a//b", "a/./b", ".git", ".GIT", "a/.Git/b",
		"node_modules", "a/node_modules", "node_modules/a", `a\b`, "a\x00b", "a\nb", strings.Repeat("a", 513),
	} {
		t.Run(directory, func(t *testing.T) {
			cfg := productionConfig()
			cfg.Checks = installCheck(&CheckInstallConfig{Ecosystem: "npm", Directories: []string{directory}})
			err := cfg.Validate()
			want := fmt.Sprintf("check web: install directory %q must be a clean repository-relative path without .., .git or node_modules components", directory)
			if err == nil || err.Error() != want {
				t.Fatalf("err = %v, want %q", err, want)
			}
		})
	}
}

func TestCheckInstallAcceptsValidSettingsAndAppliesDefaults(t *testing.T) {
	cfg := productionConfig()
	cfg.Checks = map[string]CheckConfig{
		"default": {Command: "npm", Install: &CheckInstallConfig{Ecosystem: "npm"}},
		"explicit": {Command: "npm", Install: &CheckInstallConfig{
			Ecosystem: "npm", Directories: []string{".", "packages/web", "tools/.config-x", "a-b_c.d/e"}, TimeoutSeconds: 1800,
		}},
		"minimum":     {Command: "npm", Install: &CheckInstallConfig{Ecosystem: "npm", Directories: []string{"web"}, TimeoutSeconds: 1}},
		"empty-carry": {Command: "go", DependencyDirectories: []string{}},
		"no-install":  {Command: "go"},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid install settings refused: %v", err)
	}
	install := cfg.Checks["default"].Install
	if got := install.InstallDirectories(); !slices.Equal(got, []string{"."}) {
		t.Fatalf("default directories = %v", got)
	}
	if got := install.InstallTimeoutSeconds(); got != 600 {
		t.Fatalf("default timeout = %d", got)
	}
	explicit := cfg.Checks["explicit"].Install
	if got := explicit.InstallDirectories(); !slices.Equal(got, []string{".", "packages/web", "tools/.config-x", "a-b_c.d/e"}) {
		t.Fatalf("directories = %v", got)
	}
	if got := explicit.InstallTimeoutSeconds(); got != 1800 {
		t.Fatalf("timeout = %d", got)
	}
	explicit.InstallDirectories()[0] = "changed"
	if explicit.Directories[0] != "." {
		t.Fatal("InstallDirectories returned the configuration's own slice")
	}
}

func TestLoadParsesInstallAndAcceptsAnEmptyDependencyDirectoryList(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := "control_plane_url: wss://cp.example.com/v1/runners/stream\n" +
		"job_image: registry.example.com/runner/job@sha256:" + strings.Repeat("a", 64) + "\n" +
		"checks:\n" +
		"  web:\n" +
		"    command: npm\n" +
		"    args: [test]\n" +
		"    dependency_directories: []\n" +
		"    install:\n" +
		"      ecosystem: npm\n" +
		"      directories: [., packages/web]\n" +
		"      timeout_seconds: 120\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	install := cfg.Checks["web"].Install
	if install == nil || install.Ecosystem != "npm" || !slices.Equal(install.Directories, []string{".", "packages/web"}) || install.TimeoutSeconds != 120 {
		t.Fatalf("install = %+v", install)
	}

	refused := strings.Replace(content, "dependency_directories: []", "dependency_directories: [node_modules]", 1)
	if err := os.WriteFile(path, []byte(refused), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "dependency_directories is no longer supported") {
		t.Fatalf("err = %v", err)
	}

	unknown := strings.Replace(content, "timeout_seconds: 120", "timeout_seconds: 120\n      command: yarn", 1)
	if err := os.WriteFile(path, []byte(unknown), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("an unknown install field was accepted")
	}
}

func TestNormalizeDeepCopiesTheInstallConfiguration(t *testing.T) {
	in := Config{Checks: map[string]CheckConfig{"web": {Command: "npm", Install: &CheckInstallConfig{
		Ecosystem: "npm", Directories: []string{"a", "b"}, TimeoutSeconds: 30,
	}}}}
	out := Normalize(in)
	if out.Checks["web"].Install == in.Checks["web"].Install {
		t.Fatal("Normalize kept the caller's install pointer")
	}
	out.Checks["web"].Install.Directories[1] = "changed"
	out.Checks["web"].Install.Ecosystem = "changed"
	if in.Checks["web"].Install.Directories[1] != "b" || in.Checks["web"].Install.Ecosystem != "npm" {
		t.Fatal("Normalize shares install state with the caller")
	}
	in.Checks["web"].Install.TimeoutSeconds = 99
	if out.Checks["web"].Install.TimeoutSeconds != 30 {
		t.Fatal("mutating the input after normalization changed the output")
	}
	nilInstall := Normalize(Config{Checks: map[string]CheckConfig{"go": {Command: "go"}}})
	if nilInstall.Checks["go"].Install != nil {
		t.Fatal("Normalize invented an install configuration")
	}
}
