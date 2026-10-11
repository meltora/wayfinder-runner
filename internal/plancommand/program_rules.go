package plancommand

import (
	"fmt"
	"regexp"
)

var assignmentPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

const helpVersion = " help version"

var catOptions = optionSpec{
	program: "cat", shortFlags: "AbeEnstTuv", clusters: true,
	long: longOptions("show-all number-nonblank show-ends number squeeze-blank show-tabs show-nonprinting"+helpVersion, "", ""),
}

var headTailLongOptions = longOptions("quiet silent verbose zero-terminated"+helpVersion, "bytes lines", "")

var headOptions = optionSpec{
	program: "head", shortFlags: "qvz", shortValues: "cn", numeric: true, clusters: true, long: headTailLongOptions,
}

var tailOptions = optionSpec{
	program: "tail", shortFlags: "qvz", shortValues: "cn", numeric: true, clusters: true, long: headTailLongOptions,
}

var wcOptions = optionSpec{
	program: "wc", shortFlags: "clmLw", clusters: true,
	long: longOptions("bytes chars lines max-line-length words"+helpVersion, "total", ""),
}

var lsOptions = optionSpec{
	program: "ls", shortFlags: "aAbBcCdfFgGhiklmnNopqQrRsStuUvxXZ1", shortValues: "ITw", clusters: true,
	long: longOptions(
		"all almost-all author escape ignore-backups directory dired file-type group-directories-first full-time "+
			"no-group human-readable si inode kibibytes literal numeric-uid-gid hide-control-chars show-control-chars "+
			"quote-name reverse recursive size context zero"+helpVersion,
		"block-size format hide ignore indicator-style quoting-style sort time time-style tabsize width",
		"color classify hyperlink",
	),
}

var grepOptions = optionSpec{
	program: "grep", shortFlags: "abcEFGHhiIlLnoPqrsTUvVwxyZz0123456789", shortValues: "ABCdDefm", clusters: true,
	long: longOptions(
		"extended-regexp fixed-strings basic-regexp perl-regexp ignore-case no-ignore-case word-regexp line-regexp "+
			"null-data no-messages invert-match byte-offset line-number line-buffered with-filename no-filename "+
			"only-matching quiet silent text recursive files-without-match files-with-matches count initial-tab null binary"+helpVersion,
		"regexp file max-count label binary-files directories devices include exclude exclude-from exclude-dir "+
			"before-context after-context context",
		"color colour",
	),
}

var ripgrepOptions = optionSpec{
	program: "rg", shortFlags: "abcFhHiIlnNopPqsSuUvVwx0", shortValues: "ABCEMTdefgjmrt", clusters: true,
	long: longOptions(
		"binary byte-offset case-sensitive column no-column count count-matches crlf files files-with-matches "+
			"files-without-match fixed-strings glob-case-insensitive heading no-heading hidden no-hidden ignore-case "+
			"ignore-file-case-insensitive include-zero invert-match json line-buffered line-number no-line-number "+
			"line-regexp max-columns-preview multiline multiline-dotall no-config no-filename no-ignore no-ignore-dot "+
			"no-ignore-exclude no-ignore-files no-ignore-global no-ignore-parent no-ignore-vcs no-messages no-require-git "+
			"null null-data only-matching passthru pcre2 pretty quiet smart-case stats text trim type-list unrestricted "+
			"vimgrep with-filename word-regexp"+helpVersion,
		"after-context before-context context color colors context-separator encoding engine file glob iglob "+
			"ignore-file max-columns max-count max-depth max-filesize regexp replace sort sortr threads type type-not",
		"",
	),
}

var programArgumentRules = map[string]func([]string) ([]string, error){
	"git":  checkGitArguments,
	"find": checkFindArguments,
	"rg":   ripgrepOptions.pathArguments,
	"grep": grepOptions.pathArguments,
	"ls":   lsOptions.pathArguments,
	"cat":  catOptions.pathArguments,
	"head": headOptions.pathArguments,
	"tail": tailOptions.pathArguments,
	"wc":   wcOptions.pathArguments,
}

func checkProgram(words []string) ([]string, error) {
	program := words[0]
	if assignmentPattern.MatchString(program) {
		return nil, fmt.Errorf("environment assignments are not allowed")
	}
	rule, ok := programArgumentRules[program]
	if !ok {
		return nil, fmt.Errorf("program %q is not allowed in planning jobs", program)
	}
	return rule(words[1:])
}
