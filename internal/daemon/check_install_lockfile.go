package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/example/app-runner/internal/strictjson"
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
	switch problem, key := inspectNPMLockfile(content); problem {
	case lockfileMalformed:
		return installInput{}, refuse("cannot use %s: it is not an npm lockfile of version 2 or 3", display)
	case lockfileMissingIntegrity:
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

type lockfileProblem int

const (
	lockfileValid lockfileProblem = iota
	lockfileMalformed
	lockfileMissingIntegrity
)

type npmLockedPackage struct {
	resolved  string
	integrity string
	link      bool
	inBundle  bool
	bundled   []string
	dependsOn []string
}

var (
	ssriHashPattern  = regexp.MustCompile(`^(sha1|sha256|sha384|sha512)-[A-Za-z0-9+/]+={0,2}$`)
	gitCommitPattern = regexp.MustCompile(`#([0-9a-f]{40}|[0-9a-f]{64})$`)
	dependencyFields = []string{"dependencies", "optionalDependencies", "peerDependencies"}
)

func inspectNPMLockfile(content []byte) (lockfileProblem, string) {
	top, err := strictjson.Object(content)
	if err != nil || !supportedLockfileVersion(top["lockfileVersion"]) {
		return lockfileMalformed, ""
	}
	packages, ok := lockedPackages(top["packages"])
	if !ok {
		return lockfileMalformed, ""
	}
	keys := make([]string, 0, len(packages))
	for key := range packages {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	bundles := newLockfileBundles(packages)
	for _, key := range keys {
		if installedPackageKey(key) && !packages[key].integrityBound() && !bundles.ships(key) {
			return lockfileMissingIntegrity, key
		}
	}
	return lockfileValid, ""
}

func supportedLockfileVersion(raw json.RawMessage) bool {
	if raw == nil {
		return false
	}
	number, err := strictjson.Number(raw)
	if err != nil {
		return false
	}
	version, err := number.Float64()
	return err == nil && (version == 2 || version == 3)
}

func lockedPackages(raw json.RawMessage) (map[string]npmLockedPackage, bool) {
	if raw == nil {
		return nil, false
	}
	entries, err := strictjson.Object(raw)
	if err != nil {
		return nil, false
	}
	if _, ok := entries[""]; !ok {
		return nil, false
	}
	packages := make(map[string]npmLockedPackage, len(entries))
	for key, value := range entries {
		locked, ok := decodeLockedPackage(value)
		if !ok {
			return nil, false
		}
		packages[key] = locked
	}
	return packages, true
}

func decodeLockedPackage(raw json.RawMessage) (npmLockedPackage, bool) {
	fields, err := strictjson.Object(raw)
	if err != nil {
		return npmLockedPackage{}, false
	}
	var locked npmLockedPackage
	ok := optionalLockString(fields["resolved"], &locked.resolved) &&
		optionalLockString(fields["integrity"], &locked.integrity) &&
		optionalLockBool(fields["link"], &locked.link) &&
		optionalLockBool(fields["inBundle"], &locked.inBundle) &&
		optionalLockNames(fields["bundleDependencies"], &locked.bundled)
	for _, field := range dependencyFields {
		ok = ok && appendDependencyNames(fields[field], &locked.dependsOn)
	}
	return locked, ok
}

func optionalLockNames(raw json.RawMessage, target *[]string) bool {
	var ignored bool
	if raw == nil || optionalLockBool(raw, &ignored) {
		return true
	}
	items, err := strictjson.Array(raw)
	if err != nil {
		return false
	}
	names := make([]string, 0, len(items))
	for _, item := range items {
		name, err := strictjson.String(item)
		if err != nil {
			return false
		}
		names = append(names, name)
	}
	*target = names
	return true
}

func appendDependencyNames(raw json.RawMessage, target *[]string) bool {
	if raw == nil {
		return true
	}
	dependencies, err := strictjson.Object(raw)
	if err != nil {
		return false
	}
	for name := range dependencies {
		*target = append(*target, name)
	}
	return true
}

func optionalLockString(raw json.RawMessage, target *string) bool {
	if raw == nil {
		return true
	}
	value, err := strictjson.String(raw)
	*target = value
	return err == nil
}

func optionalLockBool(raw json.RawMessage, target *bool) bool {
	switch string(bytes.TrimSpace(raw)) {
	case "":
		return raw == nil
	case "true":
		*target = true
	case "false":
	default:
		return false
	}
	return true
}

func installedPackageKey(key string) bool {
	for _, component := range strings.Split(key, "/") {
		if component == installedDependencyDirectory {
			return true
		}
	}
	return false
}

func (p npmLockedPackage) integrityBound() bool {
	if p.link || strings.HasPrefix(p.resolved, "file:") || (strings.HasPrefix(p.resolved, "git+") && gitCommitPattern.MatchString(p.resolved)) {
		return true
	}
	hashes := strings.Fields(p.integrity)
	for _, hash := range hashes {
		if !ssriHashPattern.MatchString(hash) {
			return false
		}
	}
	return len(hashes) > 0
}
