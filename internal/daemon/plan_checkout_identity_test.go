package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/example/app-runner/internal/scanner"
)

type legacyPlanBaselineWalk struct {
	root    *os.Root
	digest  hash.Hash
	entries int
}

func legacyPlanCheckoutBaseline(dir string) (string, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return "", err
	}
	defer func() { _ = root.Close() }()
	walk := &legacyPlanBaselineWalk{root: root, digest: sha256.New()}
	if err := walk.directory("."); err != nil {
		return "", err
	}
	return hex.EncodeToString(walk.digest.Sum(nil)), nil
}

func (w *legacyPlanBaselineWalk) directory(rel string) error {
	handle, err := w.root.Open(rel)
	if err != nil {
		return err
	}
	names, err := handle.Readdirnames(-1)
	closeErr := handle.Close()
	if err := errors.Join(err, closeErr); err != nil {
		return err
	}
	sort.Strings(names)
	for _, name := range names {
		child := joinRel(rel, name)
		if child == ".git" || runnerArtifactPath(child) {
			continue
		}
		if err := w.entry(child); err != nil {
			return err
		}
	}
	return nil
}

func (w *legacyPlanBaselineWalk) entry(child string) error {
	w.entries++
	if w.entries > scanner.MaxGitTreeEntries {
		return errPlanCheckoutChanged
	}
	info, err := w.root.Lstat(child)
	if err != nil {
		return err
	}
	fmt.Fprintf(w.digest, "%q\x00%s\x00", child, info.Mode().String())
	switch mode := info.Mode(); {
	case mode.IsDir():
		return w.directory(child)
	case mode&fs.ModeSymlink != 0:
		target, err := w.root.Readlink(child)
		if err != nil {
			return err
		}
		fmt.Fprintf(w.digest, "%q\x00", target)
	}
	key, ok := fileStatKey(info)
	if !ok {
		return errPlanCheckoutChanged
	}
	fmt.Fprintf(w.digest, "%d\x00%d\x00%d\x00%d\x00%d\x00%d\x00", key.dev, key.ino, key.mode, key.size, key.mtime, key.ctime)
	return nil
}

func identityFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeFixtureFile(t, dir, "README.md", "observed\n", 0o644)
	writeFixtureFile(t, dir, "private.txt", "private\n", 0o600)
	writeFixtureFile(t, dir, "src/main.go", "package main\n", 0o644)
	writeFixtureFile(t, dir, "src/run.sh", "#!/bin/sh\n", 0o755)
	writeFixtureFile(t, dir, ".git/HEAD", "ref: refs/heads/main\n", 0o644)
	writeFixtureFile(t, dir, "shortstack-artifacts/shot.png", "png", 0o600)
	if err := os.Mkdir(filepath.Join(dir, "closed"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, dir, "closed/inner.txt", "inner\n", 0o600)
	if err := os.Chmod(filepath.Join(dir, "closed"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("README.md", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	return dir
}

func mustPlanDigests(t *testing.T, dir string) planCheckoutDigests {
	t.Helper()
	digests, err := planCheckoutBaseline(dir)
	if err != nil {
		t.Fatal(err)
	}
	if digests.Strict == "" || digests.Identity == "" {
		t.Fatalf("digests = %+v", digests)
	}
	return digests
}

func untilCtimeChanges(t *testing.T, path string, change func() error) {
	t.Helper()
	before := entryCtime(t, path)
	deadline := time.Now().Add(5 * time.Second)
	for {
		time.Sleep(2 * time.Millisecond)
		if err := change(); err != nil {
			t.Fatal(err)
		}
		if entryCtime(t, path) != before {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("ctime of %s did not change", path)
		}
	}
}

func TestPlanCheckoutStrictDigestEqualsTheBaselineAlgorithm(t *testing.T) {
	for name, dir := range map[string]string{
		"fixture":     identityFixture(t),
		"git only":    emptyPlanCheckout(t),
		"missing dir": filepath.Join(t.TempDir(), "missing"),
	} {
		t.Run(name, func(t *testing.T) {
			legacy, legacyErr := legacyPlanCheckoutBaseline(dir)
			digests, err := planCheckoutBaseline(dir)
			if (legacyErr == nil) != (err == nil) {
				t.Fatalf("legacy err = %v, err = %v", legacyErr, err)
			}
			if digests.Strict != legacy {
				t.Fatalf("strict %q != legacy %q", digests.Strict, legacy)
			}
		})
	}
}

func emptyPlanCheckout(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeFixtureFile(t, dir, ".git/HEAD", "ref: refs/heads/main\n", 0o644)
	return dir
}

func TestPlanCheckoutIdentityIgnoresCtimeAndPermissionChanges(t *testing.T) {
	for name, test := range map[string]struct {
		target string
		change func(dir string) error
	}{
		"chmod to the same mode": {"README.md", func(dir string) error {
			return os.Chmod(filepath.Join(dir, "README.md"), 0o644)
		}},
		"group read on a private file": {"private.txt", func(dir string) error {
			return os.Chmod(filepath.Join(dir, "private.txt"), 0o640)
		}},
		"group read and search on a private directory": {"closed", func(dir string) error {
			return os.Chmod(filepath.Join(dir, "closed"), 0o750)
		}},
		"chown to the same owner": {"src/main.go", func(dir string) error {
			return os.Lchown(filepath.Join(dir, "src/main.go"), os.Getuid(), os.Getgid())
		}},
	} {
		t.Run(name, func(t *testing.T) {
			dir := identityFixture(t)
			before := mustPlanDigests(t, dir)
			untilCtimeChanges(t, filepath.Join(dir, test.target), func() error { return test.change(dir) })
			after := mustPlanDigests(t, dir)
			if after.Strict == before.Strict {
				t.Fatal("strict digest ignored a ctime change")
			}
			if after.Identity != before.Identity {
				t.Fatal("identity digest changed on a hand-off-only change")
			}
		})
	}
}

func TestPlanCheckoutIdentityDetectsCheckoutChanges(t *testing.T) {
	for name, change := range map[string]func(t *testing.T, dir string){
		"content with a different size": func(t *testing.T, dir string) {
			writeFixtureFile(t, dir, "README.md", "observed and changed\n", 0o644)
		},
		"same-size content with a new mtime": func(t *testing.T, dir string) {
			path := filepath.Join(dir, "README.md")
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			writeFixtureFile(t, dir, "README.md", "OBSERVED\n", 0o644)
			if err := os.Chtimes(path, time.Now(), info.ModTime().Add(time.Second)); err != nil {
				t.Fatal(err)
			}
		},
		"an added file": func(t *testing.T, dir string) {
			writeFixtureFile(t, dir, "src/added.go", "package main\n", 0o644)
		},
		"a removed file": func(t *testing.T, dir string) {
			if err := os.Remove(filepath.Join(dir, "src/main.go")); err != nil {
				t.Fatal(err)
			}
		},
		"a renamed file": func(t *testing.T, dir string) {
			if err := os.Rename(filepath.Join(dir, "src/main.go"), filepath.Join(dir, "src/moved.go")); err != nil {
				t.Fatal(err)
			}
		},
		"a new inode with the same content and mtime": func(t *testing.T, dir string) {
			path := filepath.Join(dir, "README.md")
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			replacement := filepath.Join(dir, "README.next")
			writeFixtureFile(t, dir, "README.next", "observed\n", 0o644)
			if err := os.Chtimes(replacement, info.ModTime(), info.ModTime()); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(replacement, path); err != nil {
				t.Fatal(err)
			}
		},
		"a retargeted symbolic link": func(t *testing.T, dir string) {
			if err := os.Remove(filepath.Join(dir, "link")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("private.txt", filepath.Join(dir, "link")); err != nil {
				t.Fatal(err)
			}
		},
		"a file replaced by a directory": func(t *testing.T, dir string) {
			if err := os.Remove(filepath.Join(dir, "private.txt")); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(dir, "private.txt"), 0o700); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := identityFixture(t)
			before := mustPlanDigests(t, dir)
			change(t, dir)
			after := mustPlanDigests(t, dir)
			if after.Identity == before.Identity {
				t.Fatal("identity digest missed a checkout change")
			}
			if after.Strict == before.Strict {
				t.Fatal("strict digest missed a checkout change")
			}
		})
	}
}

func TestPlanCheckoutDigestsSkipGitAndRunnerArtifacts(t *testing.T) {
	dir := identityFixture(t)
	before := mustPlanDigests(t, dir)
	writeFixtureFile(t, dir, ".git/HEAD", "0123456789012345678901234567890123456789\n", 0o644)
	writeFixtureFile(t, dir, "shortstack-artifacts/other.png", "png", 0o600)
	writeFixtureFile(t, dir, "src/Shortstack-Artifacts/nested.txt", "x", 0o600)
	if after := mustPlanDigests(t, dir); after != before {
		t.Fatalf("skipped paths changed the digests: %+v != %+v", after, before)
	}
}

func TestPlanCheckoutDigestsCoverEmptyAndLargeCheckouts(t *testing.T) {
	empty := emptyPlanCheckout(t)
	first := mustPlanDigests(t, empty)
	if second := mustPlanDigests(t, empty); second != first {
		t.Fatalf("empty checkout digests are unstable: %+v != %+v", second, first)
	}
	large := emptyPlanCheckout(t)
	for index := range 300 {
		writeFixtureFile(t, large, filepath.Join("d"+strconv.Itoa(index%7), "f"+strconv.Itoa(index)), "x", 0o644)
	}
	digests := mustPlanDigests(t, large)
	if digests == first {
		t.Fatal("a large checkout has the empty checkout's digests")
	}
}

func TestPlanCheckoutDigestsFailClosedAboveTheEntryLimit(t *testing.T) {
	dir := emptyPlanCheckout(t)
	for index := range scanner.MaxGitTreeEntries + 1 {
		file, err := os.Create(filepath.Join(dir, "f"+strconv.Itoa(index)))
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	digests, err := planCheckoutBaseline(dir)
	if !errors.Is(err, errPlanCheckoutChanged) || digests != (planCheckoutDigests{}) {
		t.Fatalf("digests = %+v err = %v", digests, err)
	}
}
