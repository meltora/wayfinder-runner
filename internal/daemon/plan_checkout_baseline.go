package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io/fs"
	"os"
	"sort"

	"github.com/example/app-runner/internal/scanner"
)

var errPlanCheckoutChanged = errors.New("plan checkout changed")

type planCheckoutDigests struct {
	Strict   string
	Identity string
}

type planBaselineWalk struct {
	root     *os.Root
	strict   hash.Hash
	identity hash.Hash
	entries  int
}

func planCheckoutBaseline(dir string) (planCheckoutDigests, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return planCheckoutDigests{}, err
	}
	defer func() { _ = root.Close() }()
	walk := &planBaselineWalk{root: root, strict: sha256.New(), identity: sha256.New()}
	if err := walk.directory("."); err != nil {
		return planCheckoutDigests{}, err
	}
	return planCheckoutDigests{
		Strict:   hex.EncodeToString(walk.strict.Sum(nil)),
		Identity: hex.EncodeToString(walk.identity.Sum(nil)),
	}, nil
}

func (w *planBaselineWalk) directory(rel string) error {
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

func (w *planBaselineWalk) entry(child string) error {
	w.entries++
	if w.entries > scanner.MaxGitTreeEntries {
		return errPlanCheckoutChanged
	}
	info, err := w.root.Lstat(child)
	if err != nil {
		return err
	}
	fmt.Fprintf(w.strict, "%q\x00%s\x00", child, info.Mode().String())
	fmt.Fprintf(w.identity, "%q\x00%s\x00", child, info.Mode().Type().String())
	switch mode := info.Mode(); {
	case mode.IsDir():
		return w.directory(child)
	case mode&fs.ModeSymlink != 0:
		target, err := w.root.Readlink(child)
		if err != nil {
			return err
		}
		fmt.Fprintf(w.strict, "%q\x00", target)
		fmt.Fprintf(w.identity, "%q\x00", target)
	}
	key, ok := fileStatKey(info)
	if !ok {
		return errPlanCheckoutChanged
	}
	fmt.Fprintf(w.strict, "%d\x00%d\x00%d\x00%d\x00%d\x00%d\x00", key.dev, key.ino, key.mode, key.size, key.mtime, key.ctime)
	fmt.Fprintf(w.identity, "%d\x00%d\x00%d\x00%d\x00", key.dev, key.ino, key.size, key.mtime)
	return nil
}

func planCheckoutHead(dir string) (string, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return "", err
	}
	defer func() { _ = root.Close() }()
	info, err := root.Lstat(".git")
	if err != nil || !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
		return "", errPlanCheckoutChanged
	}
	commit, problem := submoduleHeadCommit(root, ".git")
	if problem != submoduleReadable {
		return "", errPlanCheckoutChanged
	}
	return commit, nil
}
