package scanner

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sort"

	v1 "github.com/example/app-runner/api/runner/v1"
)

const (
	MaxFileSize = 1 << 20
	MaxItems    = 512
	MaxPathSize = 1024
)

var skippedDirectories = map[string]bool{
	".git": true, ".next": true, ".cache": true, "build": true,
	"coverage": true, "dist": true, "node_modules": true, "target": true, "vendor": true,
}

type Inventory struct {
	Items []v1.AgentConfigDiscoveryItem `json:"items"`
}

func Scan(root string) (Inventory, error) {
	inventory := Inventory{Items: []v1.AgentConfigDiscoveryItem{}}
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		return inventory, fmt.Errorf("open scan root: %w", err)
	}
	defer func() { _ = rootHandle.Close() }()

	err = fs.WalkDir(rootHandle.FS(), ".", func(relativePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if relativePath != "." && skippedDirectories[entry.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		match, recognized := matchCatalog(relativePath)
		if !recognized && entry.Type()&os.ModeSymlink != 0 {
			match, recognized = matchCatalogContainer(relativePath)
		}
		if !recognized {
			return nil
		}
		if err := validateCatalogPath(relativePath); err != nil {
			return err
		}
		item, err := scanEntry(rootHandle, relativePath, entry, match)
		if err != nil {
			return err
		}
		inventory.Items = append(inventory.Items, item)
		if len(inventory.Items) > MaxItems {
			return fmt.Errorf("inventory item limit of %d exceeded", MaxItems)
		}
		return nil
	})
	if err != nil {
		return Inventory{Items: []v1.AgentConfigDiscoveryItem{}}, fmt.Errorf("scan repository metadata: %w", err)
	}
	sort.Slice(inventory.Items, func(left, right int) bool {
		return inventory.Items[left].Path < inventory.Items[right].Path
	})
	return inventory, nil
}

func validateCatalogPath(relativePath string) error {
	if len(relativePath) > MaxPathSize {
		return fmt.Errorf("catalog path exceeds %d-byte protocol limit", MaxPathSize)
	}
	return nil
}

func scanEntry(root *os.Root, relativePath string, entry fs.DirEntry, match catalogMatch) (v1.AgentConfigDiscoveryItem, error) {
	info, err := entry.Info()
	if err != nil {
		return v1.AgentConfigDiscoveryItem{}, fmt.Errorf("inspect catalog entry %q: %w", relativePath, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return rejectedItem(relativePath, v1.AgentConfigSymlink, 0, match.provider, v1.AgentConfigPackReasonSymlink), nil
	}
	if !info.Mode().IsRegular() {
		return rejectedItem(relativePath, v1.AgentConfigBinary, info.Size(), match.provider, v1.AgentConfigPackReasonSpecialFile), nil
	}
	item := classifiedItem(relativePath, match, info.Size())
	if item.Classification == v1.AgentConfigClassRejected {
		return item, nil
	}
	if info.Size() > MaxFileSize {
		return rejectedItem(relativePath, match.sourceType, info.Size(), match.provider, v1.AgentConfigPackReasonExceedsScanSizeLimit), nil
	}
	if info.Mode()&0o111 != 0 && match.sourceType != v1.AgentConfigHook && match.sourceType != v1.AgentConfigExecutableExtension {
		match.sourceType = v1.AgentConfigExecutableExtension
		item = classifiedItem(relativePath, match, info.Size())
	}
	file, err := root.Open(relativePath)
	if err != nil {
		return rejectedItem(relativePath, match.sourceType, info.Size(), match.provider, v1.AgentConfigPackReasonUnreadableDuringScan), nil
	}
	openedInfo, statErr := file.Stat()
	data, readErr := io.ReadAll(io.LimitReader(file, MaxFileSize+1))
	finalInfo, finalStatErr := file.Stat()
	closeErr := file.Close()
	pathInfo, pathStatErr := root.Stat(relativePath)
	if statErr != nil || finalStatErr != nil || pathStatErr != nil || readErr != nil || closeErr != nil ||
		!openedInfo.Mode().IsRegular() || !finalInfo.Mode().IsRegular() || !pathInfo.Mode().IsRegular() ||
		!os.SameFile(info, openedInfo) || !os.SameFile(openedInfo, finalInfo) || !os.SameFile(finalInfo, pathInfo) ||
		openedInfo.Size() != finalInfo.Size() || openedInfo.ModTime() != finalInfo.ModTime() || len(data) > MaxFileSize {
		return rejectedItem(relativePath, match.sourceType, info.Size(), match.provider, v1.AgentConfigPackReasonUnreadableDuringScan), nil
	}
	if isBinary(data) {
		return rejectedItem(relativePath, v1.AgentConfigBinary, info.Size(), match.provider, v1.AgentConfigPackReasonBinaryContent), nil
	}
	if containsSecret(data) {
		return rejectedItem(relativePath, v1.AgentConfigCredential, info.Size(), match.provider, v1.AgentConfigPackReasonSecretContent), nil
	}
	digest := sha256.Sum256(data)
	item.DigestSHA256 = hex.EncodeToString(digest[:])
	return item, nil
}
