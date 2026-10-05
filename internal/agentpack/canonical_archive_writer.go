package agentpack

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"sort"
)

const (
	ustarBlockSize = 512
	ustarFileMode  = 0o444
)

type ArchiveContent struct {
	PackKey  string
	Provider string
	Version  string
	Files    map[string][]byte
}

type ArchiveSummary struct {
	Digest   string
	Bytes    int64
	Files    int
	Manifest ArchiveManifest
}

func WriteCanonicalArchive(out io.Writer, content ArchiveContent) (ArchiveSummary, error) {
	if out == nil {
		return ArchiveSummary{}, fmt.Errorf("Agent Pack archive output is required")
	}
	if len(content.Files) > ArchiveMaxFiles {
		return ArchiveSummary{}, fmt.Errorf("Agent Pack exceeds the %d file limit", ArchiveMaxFiles)
	}
	manifest := ArchiveManifest{
		Files: make(map[string]string, len(content.Files)), Format: ArchiveFormatVersion,
		PackKey: content.PackKey, Provider: content.Provider, Version: content.Version,
	}
	names := make([]string, 0, len(content.Files))
	var total int64
	for name, data := range content.Files {
		if !validArchivePath(name) {
			return ArchiveSummary{}, fmt.Errorf("Agent Pack path %q is invalid", name)
		}
		if len(data) > ArchiveMaxFileBytes {
			return ArchiveSummary{}, fmt.Errorf("Agent Pack file %q exceeds the file size limit", name)
		}
		total += int64(len(data))
		if total > ArchiveMaxTotalBytes {
			return ArchiveSummary{}, fmt.Errorf("Agent Pack exceeds the total size limit")
		}
		sum := sha256.Sum256(data)
		manifest.Files[name] = hex.EncodeToString(sum[:])
		names = append(names, name)
	}
	sort.Strings(names)
	if collision := archivePathCollision(append([]string{ArchiveManifestName}, names...)); collision != "" {
		return ArchiveSummary{}, fmt.Errorf("Agent Pack path %q collides with another path", collision)
	}
	manifestBytes, err := manifest.CanonicalJSON()
	if err != nil {
		return ArchiveSummary{}, err
	}
	if len(manifestBytes) > ArchiveMaxFileBytes {
		return ArchiveSummary{}, fmt.Errorf("Agent Pack manifest exceeds the file size limit")
	}
	var tape bytes.Buffer
	if err := appendUSTAREntry(&tape, ArchiveManifestName, manifestBytes); err != nil {
		return ArchiveSummary{}, err
	}
	for _, name := range names {
		if err := appendUSTAREntry(&tape, name, content.Files[name]); err != nil {
			return ArchiveSummary{}, err
		}
	}
	tape.Write(make([]byte, 2*ustarBlockSize))

	var compressed bytes.Buffer
	gz, err := gzip.NewWriterLevel(&compressed, flate.BestCompression)
	if err != nil {
		return ArchiveSummary{}, err
	}
	if _, err := gz.Write(tape.Bytes()); err != nil {
		return ArchiveSummary{}, err
	}
	if err := gz.Close(); err != nil {
		return ArchiveSummary{}, err
	}
	if int64(compressed.Len()) > ArchiveMaxCompressedBytes {
		return ArchiveSummary{}, fmt.Errorf("Agent Pack archive exceeds the compressed size limit")
	}
	sum := sha256.Sum256(compressed.Bytes())
	written, err := out.Write(compressed.Bytes())
	if err != nil {
		return ArchiveSummary{}, err
	}
	if written != compressed.Len() {
		return ArchiveSummary{}, io.ErrShortWrite
	}
	return ArchiveSummary{Digest: hex.EncodeToString(sum[:]), Bytes: int64(written), Files: len(names), Manifest: manifest}, nil
}

func appendUSTAREntry(tape *bytes.Buffer, name string, data []byte) error {
	header, err := encodeUSTARHeader(name, int64(len(data)))
	if err != nil {
		return err
	}
	tape.Write(header[:])
	tape.Write(data)
	if remainder := len(data) % ustarBlockSize; remainder != 0 {
		tape.Write(make([]byte, ustarBlockSize-remainder))
	}
	return nil
}

func encodeUSTARHeader(name string, size int64) ([ustarBlockSize]byte, error) {
	var header [ustarBlockSize]byte
	prefix, short, ok := splitUSTARName(name)
	if !ok {
		return header, fmt.Errorf("Agent Pack path %q cannot be stored in a USTAR header", name)
	}
	if size < 0 || size > ArchiveMaxFileBytes {
		return header, fmt.Errorf("Agent Pack entry size is out of range")
	}
	copy(header[0:100], short)
	copy(header[100:108], fmt.Sprintf("%07o\x00", ustarFileMode))
	copy(header[108:116], "0000000\x00")
	copy(header[116:124], "0000000\x00")
	copy(header[124:136], fmt.Sprintf("%011o\x00", size))
	copy(header[136:148], "00000000000\x00")
	header[156] = '0'
	copy(header[257:263], "ustar\x00")
	copy(header[263:265], "00")
	copy(header[329:337], "0000000\x00")
	copy(header[337:345], "0000000\x00")
	copy(header[345:500], prefix)
	copy(header[148:156], fmt.Sprintf("%06o\x00 ", ustarChecksum(header)))
	return header, nil
}

func ustarChecksum(header [ustarBlockSize]byte) int64 {
	var sum int64
	for index, value := range header {
		if index >= 148 && index < 156 {
			sum += ' '
			continue
		}
		sum += int64(value)
	}
	return sum
}

func archivePathCollision(names []string) string {
	folded := make(map[string]bool, len(names))
	for _, name := range names {
		key := asciiLower(name)
		if folded[key] {
			return name
		}
		folded[key] = true
	}
	for _, name := range names {
		key := asciiLower(name)
		for index := 0; index < len(key); index++ {
			if key[index] == '/' && folded[key[:index]] {
				return name
			}
		}
	}
	return ""
}

func asciiLower(value string) string {
	out := []byte(value)
	for index, character := range out {
		if character >= 'A' && character <= 'Z' {
			out[index] = character + ('a' - 'A')
		}
	}
	return string(out)
}
