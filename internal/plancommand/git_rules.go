package plancommand

import (
	"fmt"
	"regexp"
	"strings"
)

var gitHistoryOptions = optionSpec{
	program: "git", shortFlags: "pusmctrRabwWiEFPgzD", shortValues: "nSG", shortOptional: "UMCBl", numeric: true,
	long: longOptions(
		"oneline graph no-decorate all shortstat numstat name-only name-status patch no-patch abbrev-commit "+
			"no-abbrev-commit no-merges merges first-parent reverse follow source topo-order date-order "+
			"author-date-order all-match invert-grep regexp-ignore-case extended-regexp fixed-strings perl-regexp "+
			"left-right cherry-pick cherry-mark no-walk boundary full-history simplify-merges ancestry-path "+
			"relative-date summary raw no-color ignore-all-space ignore-space-change ignore-blank-lines full-diff "+
			"parents children cc no-renames no-textconv no-ext-diff mailmap use-mailmap quiet cached staged "+
			"minimal patience histogram function-context text full-index binary check compact-summary",
		"max-count skip since after until before author committer grep format date unified diff-filter "+
			"min-parents max-parents encoding exclude diff-algorithm word-diff-regex",
		"decorate stat dirstat branches tags remotes color word-diff abbrev relative find-renames find-copies "+
			"pretty notes",
	),
}

var gitStatusOptions = optionSpec{
	program: "git status", shortFlags: "sbvz", shortOptional: "u", clusters: true,
	long: longOptions(
		"short branch long show-stash no-column ahead-behind no-ahead-behind renames no-renames verbose null",
		"",
		"porcelain untracked-files ignored ignore-submodules column find-renames",
	),
}

var gitListFilesOptions = optionSpec{
	program: "git ls-files", shortFlags: "cdmoiskutvfz", shortValues: "xX", clusters: true,
	long: longOptions(
		"cached deleted modified others ignored stage unmerged killed directory no-empty-directory eol full-name "+
			"recurse-submodules error-unmatch exclude-standard debug deduplicate sparse",
		"format exclude exclude-from exclude-per-directory with-tree",
		"abbrev",
	),
}

var gitSignaturePlaceholder = regexp.MustCompile(`%[^A-Za-z%]*G`)

var gitSubcommandRules = map[string]optionSpec{
	"log":      gitHistoryOptions,
	"show":     gitHistoryOptions,
	"diff":     gitHistoryOptions,
	"status":   gitStatusOptions,
	"ls-files": gitListFilesOptions,
}

func checkGitArguments(args []string) ([]string, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("only git log, show, diff, status and ls-files are allowed, without global options")
	}
	spec, ok := gitSubcommandRules[args[0]]
	if !ok {
		return nil, fmt.Errorf("only git log, show, diff, status and ls-files are allowed, without global options")
	}
	for _, arg := range args[1:] {
		if (strings.HasPrefix(arg, "--format=") || strings.HasPrefix(arg, "--pretty=")) && gitSignaturePlaceholder.MatchString(arg) {
			return nil, fmt.Errorf("git signature placeholders (%%G) run GPG and are not allowed in planning jobs")
		}
	}
	return spec.pathArguments(args[1:])
}
