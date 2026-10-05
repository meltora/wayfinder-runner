package agentpack

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	ArchiveFormatVersion      = 2
	ArchiveManifestName       = "agent-pack.json"
	ArchiveMaxFiles           = 1024
	ArchiveMaxFileBytes       = 1 << 20
	ArchiveMaxTotalBytes      = 64 << 20
	ArchiveMaxCompressedBytes = DefaultMaxArchiveBytes
	ArchiveMaxPathBytes       = 255
	archiveMaxPathSlashes     = 15
	archivePackKeyMaxBytes    = 128
	archiveMaxVersionPart     = 2147483647
	archiveManifestMaxNesting = 32
	archivePrototypeSegment   = "__proto__"
)

var (
	archivePackKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$`)
	archiveVersionPattern = regexp.MustCompile(`^(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)$`)
	archiveReservedPaths  = map[string]bool{ArchiveManifestName: true, "pack.yaml": true, "signature": true}
	archiveProviders      = map[string]bool{"claude-code": true, "codex": true, "opencode": true}
	archiveManifestKeys   = []string{"files", "format", "packKey", "provider", "version"}
)

type ArchiveManifest struct {
	Files    map[string]string
	Format   int
	PackKey  string
	Provider string
	Version  string
}

func (manifest ArchiveManifest) CanonicalJSON() ([]byte, error) {
	if manifest.Format != ArchiveFormatVersion {
		return nil, fmt.Errorf("Agent Pack manifest format must be %d", ArchiveFormatVersion)
	}
	if err := validateArchiveIdentity(manifest.PackKey, manifest.Provider, manifest.Version); err != nil {
		return nil, err
	}
	if len(manifest.Files) == 0 || len(manifest.Files) > ArchiveMaxFiles {
		return nil, fmt.Errorf("Agent Pack manifest must list 1 to %d files", ArchiveMaxFiles)
	}
	names := make([]string, 0, len(manifest.Files))
	for name, digest := range manifest.Files {
		if !validArchivePath(name) {
			return nil, fmt.Errorf("Agent Pack manifest path %q is invalid", name)
		}
		if !lowerHexSHA256(digest) {
			return nil, fmt.Errorf("Agent Pack manifest digest for %q is invalid", name)
		}
		names = append(names, name)
	}
	sort.Strings(names)
	var out bytes.Buffer
	out.WriteString(`{"files":{`)
	for index, name := range names {
		if index > 0 {
			out.WriteByte(',')
		}
		out.WriteString(`"` + name + `":"` + manifest.Files[name] + `"`)
	}
	out.WriteString(`},"format":` + strconv.Itoa(ArchiveFormatVersion))
	out.WriteString(`,"packKey":"` + manifest.PackKey + `"`)
	out.WriteString(`,"provider":"` + manifest.Provider + `"`)
	out.WriteString(`,"version":"` + manifest.Version + `"}`)
	return out.Bytes(), nil
}

func ParseCanonicalManifest(raw []byte) (ArchiveManifest, error) {
	noncanonical := func(detail string) (ArchiveManifest, error) {
		return ArchiveManifest{}, newArchiveRejection(RejectManifestNoncanonical, detail)
	}
	if !utf8.Valid(raw) {
		return noncanonical("manifest is not valid UTF-8")
	}
	if manifestNestingDepth(raw) > archiveManifestMaxNesting {
		return noncanonical("manifest nesting exceeds the bound")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return noncanonical("manifest is not a JSON object")
	}
	formatRaw, ok := fields["format"]
	if !ok {
		return noncanonical("manifest format is missing")
	}
	var format any
	if err := json.Unmarshal(formatRaw, &format); err != nil {
		return noncanonical("manifest format is invalid")
	}
	number, ok := format.(float64)
	if !ok {
		return noncanonical("manifest format is not a number")
	}
	if number != ArchiveFormatVersion {
		return ArchiveManifest{}, newArchiveRejection(RejectUnsupportedFormat, "manifest format is not supported")
	}
	if len(fields) != len(archiveManifestKeys) {
		return noncanonical("manifest has unexpected keys")
	}
	for _, key := range archiveManifestKeys {
		if _, ok := fields[key]; !ok {
			return noncanonical("manifest has unexpected keys")
		}
	}
	manifest := ArchiveManifest{Format: ArchiveFormatVersion}
	if json.Unmarshal(fields["files"], &manifest.Files) != nil || manifest.Files == nil ||
		json.Unmarshal(fields["packKey"], &manifest.PackKey) != nil ||
		json.Unmarshal(fields["provider"], &manifest.Provider) != nil ||
		json.Unmarshal(fields["version"], &manifest.Version) != nil {
		return noncanonical("manifest field has the wrong type")
	}
	canonical, err := manifest.CanonicalJSON()
	if err != nil {
		return noncanonical("manifest field is invalid")
	}
	if !bytes.Equal(canonical, raw) {
		return noncanonical("manifest bytes are not canonical")
	}
	return manifest, nil
}

func validateArchiveIdentity(packKey, provider, version string) error {
	if len(packKey) > archivePackKeyMaxBytes || !archivePackKeyPattern.MatchString(packKey) {
		return fmt.Errorf("Agent Pack key must be lowercase kebab-case of at most %d bytes", archivePackKeyMaxBytes)
	}
	if !archiveProviders[provider] {
		return fmt.Errorf("Agent Pack provider is not supported")
	}
	if !stableArchiveVersion(version) {
		return fmt.Errorf("Agent Pack version must be a stable semantic version")
	}
	return nil
}

func stableArchiveVersion(value string) bool {
	if len(value) > 32 || !archiveVersionPattern.MatchString(value) {
		return false
	}
	for _, part := range strings.Split(value, ".") {
		component, err := strconv.ParseInt(part, 10, 64)
		if err != nil || component > archiveMaxVersionPart {
			return false
		}
	}
	return true
}

func lowerHexSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		if !(character >= '0' && character <= '9') && !(character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
}

func validArchivePath(name string) bool {
	if name == "" || len(name) > ArchiveMaxPathBytes || archiveReservedPaths[name] {
		return false
	}
	for index := 0; index < len(name); index++ {
		character := name[index]
		if !(character >= 'a' && character <= 'z') && !(character >= 'A' && character <= 'Z') &&
			!(character >= '0' && character <= '9') && character != '.' && character != '_' && character != '-' && character != '/' {
			return false
		}
	}
	segments := strings.Split(name, "/")
	if len(segments) > archiveMaxPathSlashes+1 {
		return false
	}
	for _, segment := range segments {
		if segment == "" || segment == "." || segment == ".." || archiveReservedSegment(segment) {
			return false
		}
	}
	_, _, ok := splitUSTARName(name)
	return ok
}

func archiveReservedSegment(segment string) bool {
	switch asciiLower(segment) {
	case ArchiveManifestName, archivePrototypeSegment, ".git", ".ds_store", "thumbs.db":
		return true
	}
	return strings.HasPrefix(segment, "._")
}

func manifestNestingDepth(raw []byte) int {
	depth, deepest := 0, 0
	inString, escaped := false, false
	for _, character := range raw {
		switch {
		case inString && escaped:
			escaped = false
		case inString && character == '\\':
			escaped = true
		case inString && character == '"':
			inString = false
		case inString:
		case character == '"':
			inString = true
		case character == '{' || character == '[':
			depth++
			deepest = max(deepest, depth)
		case (character == '}' || character == ']') && depth > 0:
			depth--
		}
	}
	return deepest
}

func splitUSTARName(name string) (string, string, bool) {
	if len(name) <= 100 {
		return "", name, true
	}
	for index := 0; index < len(name) && index <= 155; index++ {
		if name[index] == '/' && len(name)-index-1 <= 100 {
			return name[:index], name[index+1:], true
		}
	}
	return "", "", false
}
