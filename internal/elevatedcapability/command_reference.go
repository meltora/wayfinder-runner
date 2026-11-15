package elevatedcapability

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const shellMetacharacters = "$`;&|<>(){}*?[]~!#'\"\\\n\r"

var unverifiablePrograms = setOf(
	"cd", "pushd", "popd", "eval", "exec", "source", ".", "env", "xargs", "sudo", "doas", "command", "builtin",
	"nohup", "time", "timeout", "nice", "ionice", "chroot", "nsenter", "unshare", "setsid", "stdbuf", "flock",
	"watch", "script", "busybox", "toybox", "sh", "bash", "zsh", "dash", "ash", "ksh", "mksh", "csh", "tcsh",
	"fish", "nu", "xonsh", "elvish", "node", "nodejs", "deno", "python", "pypy", "ipython", "perl", "ruby",
	"irb", "jruby", "php", "lua", "luajit", "tclsh", "wish", "expect", "awk", "gawk", "mawk", "nawk", "sed",
	"osascript", "pwsh", "powershell", "cmd", "cscript", "wscript", "git", "java", "javaw", "jshell", "kotlin",
	"kotlinc", "scala", "groovy", "clojure", "clj", "rscript", "r", "julia", "erl", "escript", "elixir", "iex",
	"racket", "guile", "sbcl", "ocaml", "ghc", "runghc", "runhaskell", "dart", "docker", "podman", "nerdctl",
	"buildah", "kubectl", "ssh", "vim", "nvim", "emacs",
)

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

func setOf(values ...string) map[string]bool {
	set := make(map[string]bool, len(values))
	for _, value := range values {
		set[value] = true
	}
	return set
}

func HookCommandReason(command, workDir string) Reason {
	if strings.ContainsAny(command, shellMetacharacters) {
		return ReasonReferencedFileUnverifiable
	}
	tokens := strings.Fields(command)
	if len(tokens) == 0 || strings.Contains(tokens[0], "=") {
		return ReasonReferencedFileUnverifiable
	}
	return programReason(tokens[0], tokens[1:], workDir)
}

func hookCommandsReason(hooks map[string][]map[string]any, workDir string) Reason {
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
				if reason := HookCommandReason(text, workDir); reason != "" {
					return reason
				}
			}
		}
	}
	return ""
}

func MCPCommandReason(server map[string]any, workDir string) Reason {
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
	if reason := programReason(command, args, workDir); reason != "" {
		return reason
	}
	for _, value := range env {
		for _, part := range strings.FieldsFunc(value, func(character rune) bool {
			return character == ' ' || character == '\t' || character == ':' || character == '=' || character == ','
		}) {
			if referencesRepository(part, workDir) {
				return ReasonCommandReferencesRepositoryFile
			}
		}
	}
	return ""
}

func programReason(program string, args []string, workDir string) Reason {
	name := normalizedProgramName(program)
	if unverifiablePrograms[name] {
		return ReasonReferencedFileUnverifiable
	}
	if projectRunnerPrograms[name] {
		return ReasonCommandReferencesRepositoryFile
	}
	if !filepath.IsAbs(program) {
		if referencesRepository(program, workDir) {
			return ReasonCommandReferencesRepositoryFile
		}
		return ReasonProgramNotAbsolute
	}
	if referencesRepository(filepath.Clean(program), workDir) {
		return ReasonCommandReferencesRepositoryFile
	}
	for _, arg := range args {
		if referencesRepository(arg, workDir) {
			return ReasonCommandReferencesRepositoryFile
		}
		if _, value, found := strings.Cut(arg, "="); found && referencesRepository(value, workDir) {
			return ReasonCommandReferencesRepositoryFile
		}
	}
	return ""
}

func referencesRepository(token, workDir string) bool {
	lower := strings.ToLower(token)
	if token == "" || strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "http://") {
		return false
	}
	if filepath.IsAbs(token) {
		roots := []string{os.TempDir(), "/tmp", "/var/tmp", "/dev/shm"}
		if workDir != "" {
			roots = append(roots, workDir)
		}
		for _, root := range roots {
			if within(root, token) {
				return true
			}
		}
		return workDir == ""
	}
	if strings.HasPrefix(token, "-") && !strings.Contains(token, "/") {
		return false
	}
	if strings.ContainsAny(token, "/\\") || strings.HasPrefix(token, ".") {
		return true
	}
	if workDir == "" {
		return false
	}
	_, err := os.Lstat(filepath.Join(workDir, token))
	return err == nil
}

func within(root, target string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(target))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
