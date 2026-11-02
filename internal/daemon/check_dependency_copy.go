package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
	"syscall"
)

const installedDependencyDirectory = "node_modules"

type installedTreeScan struct {
	ctx        context.Context
	root       *os.Root
	checkID    string
	entryLimit int
	byteLimit  int64
	depthLimit int
	entries    int
	bytes      int64
}

type installedTreeSummary struct {
	entries int
	bytes   int64
	digest  string
}

type installedTreeRecord struct {
	path       string
	kind       string
	executable bool
	size       int64
	sha256     string
}

func (w *checkWorkspace) newInstalledTreeScan(ctx context.Context, root *os.Root, checkID string) *installedTreeScan {
	return &installedTreeScan{
		ctx: ctx, root: root, checkID: checkID,
		entryLimit: w.entryLimit, byteLimit: w.byteLimit, depthLimit: w.depthLimit,
	}
}

func (s *installedTreeScan) scan(repositoryRel, directory string, nestedRoots []string) (installedTreeSummary, error) {
	startEntries, startBytes := s.entries, s.bytes
	records := []installedTreeRecord{}
	for _, nested := range append([]string{""}, nestedRoots...) {
		rootRecords, err := s.scanRoot(repositoryRel, path.Join(repositoryRel, directory, nested))
		if err != nil {
			return installedTreeSummary{}, err
		}
		for _, record := range rootRecords {
			record.path = path.Join(nested, record.path)
			records = append(records, record)
		}
	}
	return installedTreeSummary{
		entries: s.entries - startEntries,
		bytes:   s.bytes - startBytes,
		digest:  installedTreeDigest(records),
	}, nil
}

func (s *installedTreeScan) scanRoot(repositoryRel, installRel string) ([]installedTreeRecord, error) {
	records := []installedTreeRecord{}
	info, err := s.root.Lstat(path.Join(installRel, installedDependencyDirectory))
	switch {
	case errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR):
		return records, nil
	case err != nil:
		return nil, err
	case info.IsDir():
		err = s.directory(repositoryRel, installRel, installedDependencyDirectory, 1, &records)
	default:
		err = s.entry(repositoryRel, installRel, installedDependencyDirectory, info, 1, &records)
	}
	return records, err
}

func (s *installedTreeScan) directory(repositoryRel, installRel, rel string, depth int, records *[]installedTreeRecord) error {
	if depth > s.depthLimit {
		return installedDependencyLimit("are more than %d directories deep", s.depthLimit)
	}
	handle, err := s.root.Open(path.Join(installRel, rel))
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
		child := rel + "/" + name
		info, err := s.root.Lstat(path.Join(installRel, child))
		if err != nil {
			return err
		}
		if err := s.entry(repositoryRel, installRel, child, info, depth, records); err != nil {
			return err
		}
	}
	return nil
}

func (s *installedTreeScan) entry(repositoryRel, installRel, rel string, info fs.FileInfo, depth int, records *[]installedTreeRecord) error {
	if err := s.ctx.Err(); err != nil {
		return err
	}
	s.entries++
	if s.entries > s.entryLimit {
		return installedDependencyLimit("have more than %d entries", s.entryLimit)
	}
	full := path.Join(installRel, rel)
	display := strings.TrimPrefix(strings.TrimPrefix(full, repositoryRel), "/")
	switch mode := info.Mode(); {
	case mode&fs.ModeSymlink != 0:
		target, err := s.root.Readlink(full)
		if err != nil {
			return err
		}
		if leavesCheckRoot(full, target) {
			return &installRefusal{message: fmt.Sprintf("dependency install for check %s produced a symbolic link that leaves the check workspace: %s", s.checkID, display)}
		}
		*records = append(*records, installedTreeRecord{path: rel, kind: "symlink", size: int64(len(target)), sha256: sha256Hex([]byte(target))})
		return nil
	case mode.IsDir():
		*records = append(*records, installedTreeRecord{path: rel, kind: "dir"})
		return s.directory(repositoryRel, installRel, rel, depth+1, records)
	case mode.IsRegular():
		s.bytes += info.Size()
		if s.bytes > s.byteLimit {
			return installedDependencyLimit("have more than %d bytes", s.byteLimit)
		}
		sum, err := s.fileDigest(full, info.Size())
		if err != nil {
			return err
		}
		*records = append(*records, installedTreeRecord{path: rel, kind: "file", executable: mode&0o111 != 0, size: info.Size(), sha256: sum})
		return nil
	default:
		return &installRefusal{message: fmt.Sprintf("dependency install for check %s produced a special file: %s", s.checkID, display)}
	}
}

func (s *installedTreeScan) fileDigest(rel string, size int64) (string, error) {
	file, err := s.root.OpenFile(rel, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	written, err := io.Copy(hash, io.LimitReader(file, size+1))
	if err != nil {
		return "", err
	}
	if written != size {
		return "", fmt.Errorf("installed file %s changed while it was scanned", rel)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func leavesCheckRoot(linkRel, target string) bool {
	if target == "" || path.IsAbs(target) {
		return true
	}
	resolved := path.Join(path.Dir(linkRel), target)
	return resolved == ".." || strings.HasPrefix(resolved, "../")
}

func installedTreeDigest(records []installedTreeRecord) string {
	sort.Slice(records, func(i, j int) bool { return records[i].path < records[j].path })
	hash := sha256.New()
	for _, record := range records {
		executable := 0
		if record.executable {
			executable = 1
		}
		_, _ = fmt.Fprintf(hash, "%s\x00%s\x00%d\x00%d\x00%s\n", record.path, record.kind, executable, record.size, record.sha256)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func installedDependencyLimit(format string, args ...any) error {
	return &installRefusal{message: "installed dependencies " + fmt.Sprintf(format, args...)}
}
