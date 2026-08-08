package updatecatalog

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var semverPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$`)

type semanticVersion struct {
	major, minor, patch uint64
	prerelease          []string
}

func parseSemanticVersion(value string) (semanticVersion, error) {
	match := semverPattern.FindStringSubmatch(value)
	if match == nil {
		return semanticVersion{}, fmt.Errorf("invalid semantic version %q", value)
	}
	parts := [3]uint64{}
	for i := range parts {
		parsed, err := strconv.ParseUint(match[i+1], 10, 64)
		if err != nil {
			return semanticVersion{}, fmt.Errorf("invalid semantic version %q", value)
		}
		parts[i] = parsed
	}
	pre := []string(nil)
	if match[4] != "" {
		pre = strings.Split(match[4], ".")
		for _, identifier := range pre {
			if numericIdentifier(identifier) && len(identifier) > 1 && identifier[0] == '0' {
				return semanticVersion{}, fmt.Errorf("invalid semantic version %q", value)
			}
		}
	}
	return semanticVersion{major: parts[0], minor: parts[1], patch: parts[2], prerelease: pre}, nil
}

func numericIdentifier(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func compareVersions(left, right semanticVersion) int {
	for _, values := range [][2]uint64{{left.major, right.major}, {left.minor, right.minor}, {left.patch, right.patch}} {
		if values[0] < values[1] {
			return -1
		}
		if values[0] > values[1] {
			return 1
		}
	}
	if len(left.prerelease) == 0 && len(right.prerelease) == 0 {
		return 0
	}
	if len(left.prerelease) == 0 {
		return 1
	}
	if len(right.prerelease) == 0 {
		return -1
	}
	for i := 0; i < len(left.prerelease) && i < len(right.prerelease); i++ {
		l, r := left.prerelease[i], right.prerelease[i]
		if l == r {
			continue
		}
		ln, rn := numericIdentifier(l), numericIdentifier(r)
		switch {
		case ln && rn:
			if len(l) < len(r) {
				return -1
			}
			if len(l) > len(r) {
				return 1
			}
			if l < r {
				return -1
			}
			return 1
		case ln:
			return -1
		case rn:
			return 1
		case l < r:
			return -1
		default:
			return 1
		}
	}
	if len(left.prerelease) < len(right.prerelease) {
		return -1
	}
	if len(left.prerelease) > len(right.prerelease) {
		return 1
	}
	return 0
}
