package daemon

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	maxLFSPointerBytes = 1024
	lfsAttributeIndex  = "attributes.index"
)

var (
	lfsPointerVersions = map[string]bool{
		"version https://git-lfs.github.com/spec/v1": true,
		"version https://hawser.github.com/spec/v1":  true,
	}
	lfsPointerKey  = regexp.MustCompile(`^[a-z0-9.-]+$`)
	lfsPointerOID  = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	lfsPointerSize = regexp.MustCompile(`^[0-9]+$`)
)

func validLFSPointer(content []byte) bool {
	if len(content) == 0 {
		return true
	}
	if len(content) > maxLFSPointerBytes || !bytes.HasSuffix(content, []byte("\n")) {
		return false
	}
	lines := strings.Split(strings.TrimSuffix(string(content), "\n"), "\n")
	if !lfsPointerVersions[lines[0]] {
		return false
	}
	values := map[string]string{}
	previous := ""
	for _, line := range lines[1:] {
		key, value, ok := strings.Cut(line, " ")
		if !ok || !lfsPointerKey.MatchString(key) || value == "" || strings.ContainsAny(value, "\r") || key <= previous {
			return false
		}
		values[key], previous = value, key
	}
	return lfsPointerOID.MatchString(values["oid"]) && lfsPointerSize.MatchString(values["size"])
}

type lfsCandidate struct {
	path string
	blob string
}

func (t *trustedResultRepository) refuseRawLFSContent(ctx context.Context, repositoryID, label string, winner agentCandidate) error {
	changed := changedRegularFiles(winner.changes)
	if len(changed) == 0 {
		return nil
	}
	tracked, err := t.lfsTrackedPaths(ctx, winner.tree, changed)
	if err != nil {
		return err
	}
	raw, err := t.rawLFSPaths(ctx, tracked)
	if err != nil {
		return err
	}
	if len(raw) == 0 {
		return nil
	}
	more := ""
	if len(raw) > 1 {
		more = fmt.Sprintf(" and %d more", len(raw)-1)
	}
	return agentResultRefusal(repositoryID, "%s has content at %s%s that .gitattributes stores in Git LFS, but the runner cannot create LFS objects; commit LFS pointers with git lfs, or remove the filter=lfs rule for these paths", label, displayEntry(raw[0]), more)
}

func changedRegularFiles(changes map[string]string) []lfsCandidate {
	var files []lfsCandidate
	for path, change := range changes {
		mode, blob, _ := strings.Cut(change, " ")
		if mode == "100644" || mode == "100755" {
			files = append(files, lfsCandidate{path: path, blob: blob})
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })
	return files
}

func (t *trustedResultRepository) lfsTrackedPaths(ctx context.Context, tree string, files []lfsCandidate) ([]lfsCandidate, error) {
	index := filepath.Join(t.root, lfsAttributeIndex)
	defer func() { _ = os.Remove(index) }()
	options := trustedGitOptions{gitDir: t.gitDir, workTree: t.filesDir, env: []string{"GIT_INDEX_FILE=" + index}}
	if _, err := t.gitAt(ctx, options, "read-tree", tree); err != nil {
		return nil, err
	}
	var input strings.Builder
	for _, file := range files {
		input.WriteString(file.path)
		input.WriteByte(0)
	}
	options.stdin = strings.NewReader(input.String())
	output, err := t.gitAt(ctx, options, "check-attr", "--cached", "-z", "--stdin", "filter")
	if err != nil {
		return nil, err
	}
	lfs := map[string]bool{}
	fields := strings.Split(string(output), "\x00")
	for index := 0; index+2 < len(fields); index += 3 {
		if fields[index+2] == "lfs" {
			lfs[fields[index]] = true
		}
	}
	var tracked []lfsCandidate
	for _, file := range files {
		if lfs[file.path] {
			tracked = append(tracked, file)
		}
	}
	return tracked, nil
}

func (t *trustedResultRepository) rawLFSPaths(ctx context.Context, tracked []lfsCandidate) ([]string, error) {
	if len(tracked) == 0 {
		return nil, nil
	}
	var input strings.Builder
	for _, file := range tracked {
		input.WriteString(file.blob)
		input.WriteByte('\n')
	}
	options := trustedGitOptions{gitDir: t.gitDir, workTree: t.filesDir, stdin: strings.NewReader(input.String())}
	sizes, err := t.gitAt(ctx, options, "cat-file", "--batch-check=%(objectname) %(objecttype) %(objectsize)")
	if err != nil {
		return nil, err
	}
	var raw []string
	var small []lfsCandidate
	for index, line := range strings.Split(strings.TrimSuffix(string(sizes), "\n"), "\n") {
		parts := strings.Fields(line)
		if index >= len(tracked) || len(parts) != 3 || parts[0] != tracked[index].blob || parts[1] != "blob" {
			return nil, errors.New("unexpected trusted object listing")
		}
		size, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil {
			return nil, errors.New("unexpected trusted object listing")
		}
		if size > maxLFSPointerBytes {
			raw = append(raw, tracked[index].path)
		} else {
			small = append(small, tracked[index])
		}
	}
	contents, err := t.smallBlobs(ctx, small)
	if err != nil {
		return nil, err
	}
	for index, content := range contents {
		if !validLFSPointer(content) {
			raw = append(raw, small[index].path)
		}
	}
	sort.Strings(raw)
	return raw, nil
}

func (t *trustedResultRepository) smallBlobs(ctx context.Context, files []lfsCandidate) ([][]byte, error) {
	if len(files) == 0 {
		return nil, nil
	}
	var input strings.Builder
	for _, file := range files {
		input.WriteString(file.blob)
		input.WriteByte('\n')
	}
	options := trustedGitOptions{gitDir: t.gitDir, workTree: t.filesDir, stdin: strings.NewReader(input.String())}
	output, err := t.gitAt(ctx, options, "cat-file", "--batch=%(objectname) %(objectsize)")
	if err != nil {
		return nil, err
	}
	reader := bufio.NewReader(bytes.NewReader(output))
	contents := make([][]byte, 0, len(files))
	for _, file := range files {
		header, err := reader.ReadString('\n')
		parts := strings.Fields(header)
		if err != nil || len(parts) != 2 || parts[0] != file.blob {
			return nil, errors.New("unexpected trusted object content")
		}
		size, err := strconv.Atoi(parts[1])
		if err != nil || size < 0 || size > maxLFSPointerBytes {
			return nil, errors.New("unexpected trusted object content")
		}
		content := make([]byte, size+1)
		if _, err := io.ReadFull(reader, content); err != nil || content[size] != '\n' {
			return nil, errors.New("unexpected trusted object content")
		}
		contents = append(contents, content[:size])
	}
	return contents, nil
}
