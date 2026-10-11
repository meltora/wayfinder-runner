package plancommand

import (
	"fmt"
	"strings"
)

var allowedFindPredicates = map[string]bool{}

func init() {
	for _, predicate := range strings.Fields(
		"-P -name -iname -path -ipath -wholename -iwholename -regex -iregex -regextype -type -maxdepth -mindepth " +
			"-size -empty -mtime -mmin -atime -amin -ctime -cmin -newer -anewer -cnewer -print -print0 -printf -ls " +
			"-prune -quit -not -a -o -and -or -true -false -readable -writable -executable -perm -user -group -uid " +
			"-gid -nouser -nogroup -links -inum -samefile -lname -ilname -depth -mount -xdev -noleaf -daystart " +
			"-nowarn -warn -ignore_readdir_race -noignore_readdir_race",
	) {
		allowedFindPredicates[predicate] = true
	}
}

func checkFindArguments(args []string) ([]string, error) {
	var paths []string
	for _, arg := range args {
		switch {
		case !strings.HasPrefix(arg, "-") || isDigits(arg[1:]) || isFindNumericValue(arg):
			paths = append(paths, arg)
		case !allowedFindPredicates[arg]:
			return nil, fmt.Errorf("find %s is not allowed in planning jobs (only exact, listed predicates are accepted)", arg)
		}
	}
	return paths, nil
}

func isFindNumericValue(arg string) bool {
	trimmed := strings.TrimRight(arg[1:], "bcwkMG")
	return trimmed != arg[1:] && len(arg[1:])-len(trimmed) == 1 && isDigits(trimmed)
}
