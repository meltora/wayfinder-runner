package scanner

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	v1 "github.com/example/app-runner/api/runner/v1"
)

const (
	MaxGitTreeBytes   = 16 << 20
	MaxGitTreeEntries = 100000
	MaxGitBlobBytes   = 16 << 20
)

var (
	ErrIncompleteScan = errors.New("repository metadata scan is incomplete")
	ErrScanLimit      = errors.New("repository metadata scan limit exceeded")
)

type gitTreeEntry struct {
	mode, oid, path string
	size            int64
}

// ScanGitTree consumes raw `ls-tree -r -l -z --full-tree` output for one pinned
// commit. readBlob must return raw object bytes, never filtered working files.
// All traversal or read failures discard the entire inventory.
func ScanGitTree(ctx context.Context, tree []byte, readBlob func(context.Context, string) ([]byte, error)) (Inventory, error) {
	inventory, err := scanGitTree(ctx, tree, readBlob)
	if err != nil {
		return Inventory{Items: []v1.AgentConfigDiscoveryItem{}}, err
	}
	return inventory, nil
}

func scanGitTree(ctx context.Context, tree []byte, readBlob func(context.Context, string) ([]byte, error)) (Inventory, error) {
	inventory := Inventory{Items: []v1.AgentConfigDiscoveryItem{}}
	if err := ctx.Err(); err != nil {
		return inventory, err
	}
	if len(tree) > MaxGitTreeBytes {
		return inventory, ErrScanLimit
	}
	if len(tree) != 0 && tree[len(tree)-1] != 0 {
		return inventory, ErrIncompleteScan
	}
	seen := make(map[string]struct{})
	totalBytes, count := int64(0), 0
	for len(tree) > 0 {
		if err := ctx.Err(); err != nil {
			return inventory, err
		}
		record, remaining, _ := bytes.Cut(tree, []byte{0})
		tree = remaining
		count++
		if count > MaxGitTreeEntries {
			return inventory, ErrScanLimit
		}
		entry, err := parseGitTreeEntry(string(record))
		if err != nil {
			return inventory, err
		}
		if _, exists := seen[entry.path]; exists {
			return inventory, ErrIncompleteScan
		}
		seen[entry.path] = struct{}{}
		if skipGitPath(entry.path) {
			continue
		}
		match, recognized := matchCatalog(entry.path)
		if !recognized && entry.mode == "120000" {
			match, recognized = matchCatalogContainer(entry.path)
		}
		if !recognized {
			continue
		}
		if len(inventory.Items) == MaxItems {
			return inventory, ErrScanLimit
		}
		item, readBytes, err := scanGitEntry(ctx, entry, match, MaxGitBlobBytes-totalBytes, readBlob)
		if err != nil {
			return inventory, err
		}
		totalBytes += readBytes
		inventory.Items = append(inventory.Items, item)
	}
	if err := ctx.Err(); err != nil {
		return inventory, err
	}
	sort.Slice(inventory.Items, func(a, b int) bool { return inventory.Items[a].Path < inventory.Items[b].Path })
	return inventory, nil
}

func parseGitTreeEntry(record string) (gitTreeEntry, error) {
	header, path, ok := strings.Cut(record, "\t")
	fields := strings.Fields(header)
	if !ok || len(fields) != 4 || fields[1] != "blob" ||
		(fields[0] != "100644" && fields[0] != "100755" && fields[0] != "120000") {
		// Gitlinks deliberately fail even under a normally skipped directory:
		// a parent repository cannot attest to submodule contents.
		return gitTreeEntry{}, ErrIncompleteScan
	}
	if len(path) > MaxPathSize {
		return gitTreeEntry{}, ErrScanLimit
	}
	if path == "" || !utf8.ValidString(path) || strings.ContainsAny(path, "\\\x00") {
		return gitTreeEntry{}, ErrIncompleteScan
	}
	for _, part := range strings.Split(path, "/") {
		if part == "" || part == "." || part == ".." {
			return gitTreeEntry{}, ErrIncompleteScan
		}
	}
	oid := fields[2]
	if len(oid) != 40 && len(oid) != 64 {
		return gitTreeEntry{}, ErrIncompleteScan
	}
	if _, err := hex.DecodeString(oid); err != nil || strings.ToLower(oid) != oid {
		return gitTreeEntry{}, ErrIncompleteScan
	}
	size, err := strconv.ParseInt(fields[3], 10, 64)
	if err != nil || size < 0 {
		return gitTreeEntry{}, ErrIncompleteScan
	}
	return gitTreeEntry{mode: fields[0], oid: oid, path: path, size: size}, nil
}

func skipGitPath(path string) bool {
	parts := strings.Split(path, "/")
	for _, part := range parts[:len(parts)-1] {
		if skippedDirectories[part] {
			return true
		}
	}
	return false
}

func scanGitEntry(ctx context.Context, entry gitTreeEntry, match catalogMatch, remaining int64, readBlob func(context.Context, string) ([]byte, error)) (v1.AgentConfigDiscoveryItem, int64, error) {
	if entry.mode == "120000" {
		return rejectedItem(entry.path, v1.AgentConfigSymlink, 0, match.provider, v1.AgentConfigPackReasonSymlink), 0, nil
	}
	item := classifiedItem(entry.path, match, entry.size)
	if item.Classification == v1.AgentConfigClassRejected {
		return item, 0, nil
	}
	if entry.size > MaxFileSize {
		return rejectedItem(entry.path, match.sourceType, entry.size, match.provider, v1.AgentConfigPackReasonExceedsScanSizeLimit), 0, nil
	}
	if entry.mode == "100755" && match.sourceType != v1.AgentConfigHook && match.sourceType != v1.AgentConfigExecutableExtension {
		match.sourceType = v1.AgentConfigExecutableExtension
		item = classifiedItem(entry.path, match, entry.size)
	}
	if entry.size > remaining {
		return item, 0, ErrScanLimit
	}
	if readBlob == nil {
		return item, 0, ErrIncompleteScan
	}
	data, err := readBlob(ctx, entry.oid)
	if err != nil {
		return item, 0, fmt.Errorf("%w: %w", ErrIncompleteScan, err)
	}
	if err := ctx.Err(); err != nil {
		return item, 0, err
	}
	if len(data) > MaxFileSize {
		return item, 0, ErrScanLimit
	}
	if int64(len(data)) != entry.size {
		return item, 0, ErrIncompleteScan
	}
	if isBinary(data) {
		return rejectedItem(entry.path, v1.AgentConfigBinary, entry.size, match.provider, v1.AgentConfigPackReasonBinaryContent), entry.size, nil
	}
	if containsSecret(data) {
		return rejectedItem(entry.path, v1.AgentConfigCredential, entry.size, match.provider, v1.AgentConfigPackReasonSecretContent), entry.size, nil
	}
	digest := sha256.Sum256(data)
	item.DigestSHA256 = hex.EncodeToString(digest[:])
	return item, entry.size, nil
}
