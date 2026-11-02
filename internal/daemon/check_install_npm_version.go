package daemon

import (
	"regexp"
	"strconv"
)

const minimumNPMVersionID = "10.9.4"

var (
	minimumNPMVersion = npmVersion{core: [3]int{10, 9, 4}}
	npmVersionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)
)

type npmVersion struct {
	core       [3]int
	prerelease bool
}

func parseNPMVersion(value string) (npmVersion, bool) {
	match := npmVersionPattern.FindStringSubmatch(value)
	if match == nil {
		return npmVersion{}, false
	}
	var version npmVersion
	for index := range version.core {
		number, err := strconv.Atoi(match[index+1])
		if err != nil {
			return npmVersion{}, false
		}
		version.core[index] = number
	}
	version.prerelease = match[4] != ""
	return version, true
}

func (v npmVersion) atLeast(minimum npmVersion) bool {
	for index := range v.core {
		if v.core[index] != minimum.core[index] {
			return v.core[index] > minimum.core[index]
		}
	}
	return !v.prerelease || minimum.prerelease
}
