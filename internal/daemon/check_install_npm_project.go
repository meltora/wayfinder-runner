package daemon

import (
	"path"
	"sort"
	"strings"

	"github.com/example/app-runner/internal/strictjson"
)

const npmPackageManagerPrefix = "npm@"

type npmManifest struct {
	bundles           bool
	npmPackageManager bool
}

func parseNPMManifest(content []byte) (npmManifest, bool) {
	fields, err := strictjson.Object(content)
	if err != nil {
		return npmManifest{}, false
	}
	manifest := npmManifest{bundles: declaresBundle(fields), npmPackageManager: true}
	if raw, ok := fields["packageManager"]; ok {
		value, err := strictjson.String(raw)
		manifest.npmPackageManager = err == nil && strings.HasPrefix(value, npmPackageManagerPrefix) && len(value) > len(npmPackageManagerPrefix)
	}
	return manifest, true
}

func lockfileInstallRoots(packages map[string]npmLockedPackage) []string {
	roots := map[string]bool{}
	for key := range packages {
		if key == "" {
			continue
		}
		components := strings.Split(key, "/")
		end := len(components)
		for index, component := range components {
			if component == installedDependencyDirectory {
				end = index
				break
			}
		}
		root := strings.Join(components[:end], "/")
		if root != "" && localInstallRoot(root) {
			roots[root] = true
		}
	}
	sorted := make([]string, 0, len(roots))
	for root := range roots {
		sorted = append(sorted, root)
	}
	sort.Strings(sorted)
	return sorted
}

func localInstallRoot(root string) bool {
	if path.IsAbs(root) || path.Clean(root) != root {
		return false
	}
	for _, component := range strings.Split(root, "/") {
		if component == ".." || component == "." || strings.EqualFold(component, ".git") {
			return false
		}
	}
	return true
}
