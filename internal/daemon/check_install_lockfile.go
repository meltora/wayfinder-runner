package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
)

const (
	maxInstallInputBytes   = 64 << 20
	npmShrinkwrapFile      = "npm-shrinkwrap.json"
	npmPackageLockFile     = "package-lock.json"
	npmPackageManifestFile = "package.json"
)

type installInput struct {
	directory          string
	lockfile           string
	lockfileBlobSHA    string
	packageJSONBlobSHA string
}

type publishedEntry struct {
	mode string
	kind string
	sha  string
	size int64
}

type npmLockfile struct {
	LockfileVersion any                         `json:"lockfileVersion"`
	Packages        map[string]npmLockedPackage `json:"packages"`
}

type npmLockedPackage struct {
	Resolved  string `json:"resolved"`
	Integrity string `json:"integrity"`
	Link      bool   `json:"link"`
}

type installRefusal struct {
	message string
}

func (e *installRefusal) Error() string {
	return e.message
}

func displayInstallDirectory(directory string) string {
	if directory == "." {
		return "the repository root"
	}
	return directory
}

func readInstallInput(ctx context.Context, trusted *trustedResultRepository, checkID, commit, directory string, limit int64) (installInput, error) {
	refuse := func(format string, args ...any) error {
		return &installRefusal{message: "dependency install for check " + checkID + " " + fmt.Sprintf(format, args...)}
	}
	entries, err := publishedEntries(ctx, trusted, commit, directory,
		npmShrinkwrapFile, npmPackageLockFile, npmPackageManifestFile, installedDependencyDirectory)
	if err != nil {
		return installInput{}, err
	}
	input := installInput{directory: directory, lockfile: npmPackageLockFile}
	if _, ok := entries[npmShrinkwrapFile]; ok {
		input.lockfile = npmShrinkwrapFile
	}
	lockfile, hasLockfile := entries[input.lockfile]
	manifest, hasManifest := entries[npmPackageManifestFile]
	switch {
	case !hasLockfile:
		return installInput{}, refuse("needs %s or %s in %s; publish the lockfile", npmPackageLockFile, npmShrinkwrapFile, displayInstallDirectory(directory))
	case !hasManifest:
		return installInput{}, refuse("needs %s in %s", npmPackageManifestFile, displayInstallDirectory(directory))
	}
	if _, ok := entries[installedDependencyDirectory]; ok {
		return installInput{}, refuse("cannot install into %s: the published tree has node_modules there; the runner installs it from the lockfile", displayInstallDirectory(directory))
	}
	for _, file := range []struct {
		name  string
		entry publishedEntry
	}{{input.lockfile, lockfile}, {npmPackageManifestFile, manifest}} {
		display := path.Join(directory, file.name)
		if file.entry.kind != "blob" || (file.entry.mode != "100644" && file.entry.mode != "100755") {
			return installInput{}, refuse("cannot use %s: it is not a regular file in the published tree", display)
		}
		if file.entry.size > limit {
			return installInput{}, refuse("cannot use %s: it is larger than %d bytes", display, limit)
		}
	}
	content, err := trusted.gitAt(ctx, trustedGitOptions{gitDir: trusted.gitDir, limit: int(lockfile.size) + 1}, "cat-file", "blob", lockfile.sha)
	if err != nil {
		return installInput{}, err
	}
	display := path.Join(directory, input.lockfile)
	parsed, ok := parseNPMLockfile(content)
	if !ok {
		return installInput{}, refuse("cannot use %s: it is not an npm lockfile of version 2 or 3", display)
	}
	if key, missing := packageWithoutIntegrity(parsed); missing {
		return installInput{}, refuse("cannot use %s: the registry package %s has no integrity", display, key)
	}
	input.lockfileBlobSHA, input.packageJSONBlobSHA = lockfile.sha, manifest.sha
	return input, nil
}

func publishedEntries(ctx context.Context, trusted *trustedResultRepository, commit, directory string, names ...string) (map[string]publishedEntry, error) {
	paths := make([]string, 0, len(names))
	for _, name := range names {
		paths = append(paths, path.Join(directory, name))
	}
	options := trustedGitOptions{gitDir: trusted.gitDir, env: []string{"GIT_LITERAL_PATHSPECS=1"}}
	raw, err := trusted.gitAt(ctx, options, append([]string{"ls-tree", "-z", "--long", "--full-tree", commit, "--"}, paths...)...)
	if err != nil {
		return nil, err
	}
	entries := map[string]publishedEntry{}
	for _, record := range bytes.Split(raw, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		meta, name, ok := strings.Cut(string(record), "\t")
		fields := strings.Fields(meta)
		if !ok || len(fields) != 4 {
			return nil, fmt.Errorf("unexpected ls-tree record")
		}
		entry := publishedEntry{mode: fields[0], kind: fields[1], sha: fields[2], size: -1}
		if fields[3] != "-" {
			if entry.size, err = strconv.ParseInt(fields[3], 10, 64); err != nil {
				return nil, fmt.Errorf("unexpected ls-tree size")
			}
		}
		entries[path.Base(name)] = entry
	}
	return entries, nil
}

func parseNPMLockfile(content []byte) (npmLockfile, bool) {
	var parsed npmLockfile
	if err := json.Unmarshal(content, &parsed); err != nil {
		return npmLockfile{}, false
	}
	version, ok := parsed.LockfileVersion.(float64)
	if !ok || (version != 2 && version != 3) {
		return npmLockfile{}, false
	}
	return parsed, true
}

func packageWithoutIntegrity(lockfile npmLockfile) (string, bool) {
	keys := make([]string, 0, len(lockfile.Packages))
	for key := range lockfile.Packages {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		locked := lockfile.Packages[key]
		resolved := strings.ToLower(locked.Resolved)
		remote := strings.HasPrefix(resolved, "http://") || strings.HasPrefix(resolved, "https://")
		if remote && !locked.Link && strings.TrimSpace(locked.Integrity) == "" {
			return key, true
		}
	}
	return "", false
}
