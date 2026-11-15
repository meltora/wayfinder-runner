package elevatedcapability

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const shellMetacharacters = "$`;&|<>(){}*?[]~!#'\"\\\n\r"

var unverifiablePrograms = setOf(
	"cd", "pushd", "popd", "eval", "exec", "source", ".", "env", "command", "builtin", "time", "setsid",
	"busybox", "toybox", "sh", "bash", "zsh", "dash", "ash", "ksh", "mksh", "csh", "tcsh",
	"fish", "nu", "xonsh", "elvish", "node", "nodejs", "deno", "python", "pypy", "ipython", "perl", "ruby",
	"irb", "jruby", "php", "lua", "luajit", "tclsh", "wish", "expect", "awk", "gawk", "mawk", "nawk", "sed",
	"osascript", "pwsh", "powershell", "cmd", "cscript", "wscript", "git", "java", "javaw", "jshell", "kotlin",
	"kotlinc", "scala", "groovy", "clojure", "clj", "rscript", "r", "julia", "erl", "escript", "elixir", "iex",
	"racket", "guile", "sbcl", "ocaml", "ghc", "runghc", "runhaskell", "dart", "docker", "podman", "nerdctl",
	"buildah", "kubectl", "ssh", "vim", "nvim", "emacs",
)

var wrapperPrograms = setOf(
	"strace", "ltrace", "taskset", "prlimit", "chrt", "nice", "ionice", "setpriv", "runuser", "su", "sudo", "doas",
	"sg", "systemd-run", "bwrap", "firejail", "gdb", "valgrind", "faketime", "timeout", "xargs", "run-parts",
	"nohup", "stdbuf", "unshare", "nsenter", "chroot", "flock", "watch", "script", "perf", "numactl", "cpulimit",
	"setarch", "newgrp", "pkexec", "sudoedit", "capsh", "chpst", "daemonize", "start-stop-daemon", "catchsegv",
	"ld-linux", "ld-linux-x86-64.so", "ld.so", "proot", "fakeroot", "fakechroot", "torsocks", "proxychains",
)

var interpreterPrefixes = []string{
	"python", "pypy", "perl", "ruby", "node", "php", "lua", "java", "bash", "zsh", "dash", "ksh", "fish",
}

var projectRunnerPrograms = setOf(
	"npm", "npx", "pnpm", "pnpx", "yarn", "bun", "bunx", "corepack", "make", "gmake", "just", "task", "go",
	"cargo", "rustc", "gradle", "gradlew", "mvn", "mvnw", "ant", "sbt", "lein", "rake", "bundle", "rspec",
	"rubocop", "composer", "phpunit", "poetry", "pipenv", "pip", "pdm", "hatch", "rye", "uv", "uvx", "tox",
	"nox", "pytest", "black", "ruff", "flake", "mypy", "pylint", "isort", "jest", "vitest", "mocha", "ava",
	"playwright", "cypress", "tsx", "ts-node", "tsc", "eslint", "prettier", "biome", "stylelint", "oxlint",
	"vite", "webpack", "rollup", "esbuild", "rspack", "parcel", "swc", "babel", "turbo", "nx", "lerna",
	"pre-commit", "prek", "lefthook", "husky", "lint-staged", "overcommit", "terraform", "tofu", "terragrunt",
	"pulumi", "ansible", "ansible-playbook", "cmake", "ctest", "ninja", "meson", "scons", "bazel", "bazelisk",
	"buck", "please", "pants", "dotnet", "msbuild", "mix", "swift", "stack", "cabal", "golangci-lint",
	"goreleaser", "air", "docker-compose", "skaffold", "tilt", "helm", "kustomize", "earthly", "dagger",
)

var programSuffixes = []string{".exe", ".cmd", ".bat", ".ps1"}

var deviceRoots = []string{"/proc", "/dev/fd", "/dev/stdin", "/dev/stdout", "/dev/stderr"}

func normalizedProgramName(program string) string {
	name := strings.ToLower(filepath.Base(program))
	for _, suffix := range programSuffixes {
		name = strings.TrimSuffix(name, suffix)
	}
	if trimmed := strings.TrimRight(name, "0123456789.-"); trimmed != "" {
		return trimmed
	}
	return name
}

func hasInterpreterPrefix(name string) bool {
	for _, prefix := range interpreterPrefixes {
		rest, found := strings.CutPrefix(name, prefix)
		if found && (rest == "" || strings.ContainsAny(rest[:1], "0123456789-.")) {
			return true
		}
	}
	return false
}

func nameReason(token string) Reason {
	name := normalizedProgramName(token)
	switch {
	case unverifiablePrograms[name], wrapperPrograms[name], hasInterpreterPrefix(name):
		return ReasonReferencedFileUnverifiable
	case projectRunnerPrograms[name]:
		return ReasonCommandReferencesRepositoryFile
	}
	return ""
}

func setOf(values ...string) map[string]bool {
	set := make(map[string]bool, len(values))
	for _, value := range values {
		set[value] = true
	}
	return set
}

type commandScope struct {
	workDir string
	roots   []string
}

func newCommandScope(workDir string, protectedDirs []string) commandScope {
	candidates := append([]string{os.TempDir(), "/tmp", "/var/tmp", "/dev/shm"}, deviceRoots...)
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, home)
	}
	if cache, err := os.UserCacheDir(); err == nil {
		candidates = append(candidates, cache)
	}
	if config, err := os.UserConfigDir(); err == nil {
		candidates = append(candidates, config)
	}
	if workDir != "" {
		candidates = append(candidates, workDir, filepath.Dir(filepath.Clean(workDir)))
	}
	candidates = append(candidates, protectedDirs...)
	scope := commandScope{workDir: workDir}
	for _, candidate := range candidates {
		for _, root := range lexicalAndResolved(candidate) {
			if root != string(filepath.Separator) {
				scope.roots = append(scope.roots, root)
			}
		}
	}
	return scope
}

func lexicalAndResolved(path string) []string {
	if path == "" || !filepath.IsAbs(path) {
		return nil
	}
	clean := filepath.Clean(path)
	paths := []string{clean}
	if resolved, err := filepath.EvalSymlinks(clean); err == nil && resolved != clean {
		paths = append(paths, resolved)
	}
	return paths
}

func (scope commandScope) refusedAbsolute(path string) bool {
	for _, candidate := range lexicalAndResolved(path) {
		for _, root := range scope.roots {
			if within(root, candidate) {
				return true
			}
		}
	}
	return false
}

func HookCommandReason(command, workDir string) Reason {
	return newCommandScope(workDir, nil).hookCommandReason(command)
}

func (scope commandScope) hookCommandReason(command string) Reason {
	if strings.ContainsAny(command, shellMetacharacters) {
		return ReasonReferencedFileUnverifiable
	}
	tokens := strings.Fields(command)
	if len(tokens) == 0 || strings.Contains(tokens[0], "=") {
		return ReasonReferencedFileUnverifiable
	}
	return scope.programReason(tokens[0], tokens[1:])
}

func (scope commandScope) hookCommandsReason(hooks map[string][]map[string]any) Reason {
	events := make([]string, 0, len(hooks))
	for event := range hooks {
		events = append(events, event)
	}
	sort.Strings(events)
	for _, event := range events {
		for _, group := range hooks[event] {
			commands, _ := group["hooks"].([]map[string]any)
			for _, command := range commands {
				text, _ := command["command"].(string)
				if reason := scope.hookCommandReason(text); reason != "" {
					return reason
				}
			}
		}
	}
	return ""
}

func MCPCommandReason(server map[string]any, workDir string) Reason {
	return newCommandScope(workDir, nil).mcpCommandReason(server)
}

func (scope commandScope) mcpCommandReason(server map[string]any) Reason {
	command, _ := server["command"].(string)
	if command == "" {
		return ""
	}
	args, _ := server["args"].([]string)
	values := append([]string{command}, args...)
	env, _ := server["env"].(map[string]string)
	for _, value := range env {
		values = append(values, value)
	}
	for _, value := range values {
		if strings.ContainsAny(value, "$`~") {
			return ReasonReferencedFileUnverifiable
		}
	}
	if reason := scope.programReason(command, args); reason != "" {
		return reason
	}
	for _, value := range env {
		if scope.envValueReferencesRefusedRoot(value) {
			return ReasonCommandReferencesRepositoryFile
		}
	}
	return ""
}

func (scope commandScope) envValueReferencesRefusedRoot(value string) bool {
	for _, root := range deviceRoots {
		if strings.Contains(value, root) {
			return true
		}
	}
	for _, part := range strings.FieldsFunc(value, func(character rune) bool {
		return character == ' ' || character == '\t' || character == ':' || character == '=' || character == ','
	}) {
		if scope.referencesRepository(part) {
			return true
		}
	}
	return false
}

func (scope commandScope) programReason(program string, args []string) Reason {
	if reason := nameReason(program); reason != "" {
		return reason
	}
	if !filepath.IsAbs(program) {
		if scope.referencesRepository(program) {
			return ReasonCommandReferencesRepositoryFile
		}
		return ReasonProgramNotAbsolute
	}
	if scope.referencesRepository(program) {
		return ReasonCommandReferencesRepositoryFile
	}
	for _, arg := range args {
		if reason := scope.argumentReason(arg); reason != "" {
			return reason
		}
	}
	return ""
}

func (scope commandScope) argumentReason(arg string) Reason {
	tokens := []string{arg}
	if _, value, found := strings.Cut(arg, "="); found {
		tokens = append(tokens, value)
	}
	for _, token := range tokens {
		if isURL(token) {
			continue
		}
		if reason := nameReason(token); reason != "" {
			return reason
		}
		if scope.referencesRepository(token) {
			return ReasonCommandReferencesRepositoryFile
		}
	}
	return ""
}

func isURL(token string) bool {
	lower := strings.ToLower(token)
	return strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "http://")
}

func (scope commandScope) referencesRepository(token string) bool {
	if token == "" || isURL(token) {
		return false
	}
	if filepath.IsAbs(token) {
		return scope.workDir == "" || scope.refusedAbsolute(token)
	}
	if strings.HasPrefix(token, "-") && !strings.Contains(token, "/") {
		return false
	}
	if strings.ContainsAny(token, "/\\") || strings.HasPrefix(token, ".") {
		return true
	}
	if scope.workDir == "" {
		return false
	}
	_, err := os.Lstat(filepath.Join(scope.workDir, token))
	return err == nil
}

func within(root, target string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(target))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
