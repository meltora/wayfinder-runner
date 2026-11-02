package daemon

import (
	"slices"
	"strings"
)

const installedKeyMarker = installedDependencyDirectory + "/"

type lockfileBundles struct {
	packages   map[string]npmLockedPackage
	dependents map[string][]string
	bundlers   map[string]string
	visiting   map[string]bool
}

func newLockfileBundles(packages map[string]npmLockedPackage) *lockfileBundles {
	bundles := &lockfileBundles{
		packages:   packages,
		dependents: map[string][]string{},
		bundlers:   map[string]string{},
		visiting:   map[string]bool{},
	}
	for key, locked := range packages {
		for _, name := range locked.dependsOn {
			if target, ok := bundles.resolve(key, name); ok {
				bundles.dependents[target] = append(bundles.dependents[target], key)
			}
		}
	}
	return bundles
}

func (b *lockfileBundles) ships(key string) bool {
	return b.packages[key].inBundle && b.bundler(key) != ""
}

func (b *lockfileBundles) bundler(key string) string {
	if bundler, ok := b.bundlers[key]; ok {
		return bundler
	}
	if b.visiting[key] {
		return ""
	}
	b.visiting[key] = true
	bundler := b.findBundler(key)
	b.bundlers[key] = bundler
	return bundler
}

func (b *lockfileBundles) findBundler(key string) string {
	parent, name, ok := splitInstalledKey(key)
	if !ok || !installedPackageKey(parent) {
		return ""
	}
	parentEntry, exists := b.packages[parent]
	if !exists || parentEntry.link {
		return ""
	}
	if bundler := b.bundler(parent); bundler != "" {
		return bundler
	}
	if slices.Contains(parentEntry.bundled, name) {
		return parent
	}
	for _, dependent := range b.dependents[key] {
		if b.bundler(dependent) == parent {
			return parent
		}
	}
	return ""
}

func (b *lockfileBundles) resolve(from, name string) (string, bool) {
	current := from
	for {
		candidate := installedKeyMarker + name
		if current != "" {
			candidate = current + "/" + candidate
		}
		if _, ok := b.packages[candidate]; ok {
			return candidate, true
		}
		if current == "" {
			return "", false
		}
		current, _, _ = splitInstalledKey(current)
	}
}

func splitInstalledKey(key string) (string, string, bool) {
	if index := strings.LastIndex(key, "/"+installedKeyMarker); index >= 0 {
		return key[:index], key[index+1+len(installedKeyMarker):], true
	}
	if name, ok := strings.CutPrefix(key, installedKeyMarker); ok {
		return "", name, true
	}
	return "", "", false
}
