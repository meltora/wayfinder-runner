package agentpack

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"unicode/utf8"
)

type ArchiveBuildOptions struct {
	PackKey  string
	Provider string
	Version  string
}

func BuildArchiveFromDirectory(source string, options ArchiveBuildOptions, out io.Writer) (ArchiveSummary, error) {
	if err := validateArchiveIdentity(options.PackKey, options.Provider, options.Version); err != nil {
		return ArchiveSummary{}, err
	}
	files, err := ReadArchiveSource(source)
	if err != nil {
		return ArchiveSummary{}, err
	}
	return WriteCanonicalArchive(out, ArchiveContent{
		PackKey: options.PackKey, Provider: options.Provider, Version: options.Version, Files: files,
	})
}

func ReadArchiveSource(source string) (map[string][]byte, error) {
	info, err := os.Lstat(source)
	if err != nil {
		return nil, fmt.Errorf("open Agent Pack source: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("Agent Pack source must be a real directory")
	}
	root, err := os.OpenRoot(source)
	if err != nil {
		return nil, fmt.Errorf("open Agent Pack source: %w", err)
	}
	defer func() { _ = root.Close() }()
	files := map[string][]byte{}
	var total int64
	err = fs.WalkDir(root.FS(), ".", func(relative string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("Agent Pack source path %q cannot be read", relative)
		}
		if relative == "." {
			return nil
		}
		if !utf8.ValidString(relative) || !archiveSourcePathAllowed(relative) {
			return fmt.Errorf("Agent Pack source path %q is invalid", relative)
		}
		if reason := archiveSourceRefusal(relative); reason != "" {
			return fmt.Errorf("Agent Pack source path %q %s", relative, reason)
		}
		mode := entry.Type()
		if mode&fs.ModeSymlink != 0 {
			return fmt.Errorf("Agent Pack source path %q is a symbolic link", relative)
		}
		if entry.IsDir() {
			return nil
		}
		if !mode.IsRegular() {
			return fmt.Errorf("Agent Pack source path %q is not a regular file", relative)
		}
		if archiveReservedPaths[relative] {
			return fmt.Errorf("Agent Pack source path %q uses a reserved name", relative)
		}
		if !validArchivePath(relative) {
			return fmt.Errorf("Agent Pack source path %q is invalid", relative)
		}
		if len(files) >= ArchiveMaxFiles {
			return fmt.Errorf("Agent Pack source exceeds the %d file limit", ArchiveMaxFiles)
		}
		data, err := readArchiveSourceFile(root, relative)
		if err != nil {
			return err
		}
		total += int64(len(data))
		if total > ArchiveMaxTotalBytes {
			return fmt.Errorf("Agent Pack source exceeds the total size limit")
		}
		files[relative] = data
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("Agent Pack source contains no files")
	}
	return files, nil
}

func archiveSourcePathAllowed(relative string) bool {
	for _, segment := range strings.Split(relative, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
		for index := 0; index < len(segment); index++ {
			character := segment[index]
			if !(character >= 'a' && character <= 'z') && !(character >= 'A' && character <= 'Z') &&
				!(character >= '0' && character <= '9') && character != '.' && character != '_' && character != '-' {
				return false
			}
		}
	}
	return true
}

func archiveSourceRefusal(relative string) string {
	name := relative[strings.LastIndexByte(relative, '/')+1:]
	folded := asciiLower(name)
	switch {
	case folded == ".git":
		return "is version-control metadata"
	case folded == ".ds_store" || folded == "thumbs.db" || strings.HasPrefix(name, "._"):
		return "is operating-system metadata"
	case folded == ArchiveManifestName:
		return "uses the reserved manifest name"
	case folded == archivePrototypeSegment:
		return "uses the reserved __proto__ name"
	}
	return ""
}

func readArchiveSourceFile(root *os.Root, relative string) ([]byte, error) {
	before, err := root.Lstat(relative)
	if err != nil || !before.Mode().IsRegular() {
		return nil, fmt.Errorf("Agent Pack source path %q is not a regular file", relative)
	}
	if before.Size() > ArchiveMaxFileBytes {
		return nil, fmt.Errorf("Agent Pack source path %q exceeds the file size limit", relative)
	}
	file, err := root.Open(relative)
	if err != nil {
		return nil, fmt.Errorf("Agent Pack source path %q cannot be opened", relative)
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		return nil, fmt.Errorf("Agent Pack source path %q changed while it was read", relative)
	}
	data, err := io.ReadAll(io.LimitReader(file, ArchiveMaxFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("Agent Pack source path %q cannot be read", relative)
	}
	if len(data) > ArchiveMaxFileBytes {
		return nil, fmt.Errorf("Agent Pack source path %q exceeds the file size limit", relative)
	}
	return data, nil
}
