package agentpack

import (
	"bytes"
	"compress/flate"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
)

type ArchiveRejectionReason string

const (
	RejectGzipHeader           ArchiveRejectionReason = "gzip_header"
	RejectGzipMembers          ArchiveRejectionReason = "gzip_members"
	RejectGzipTrailing         ArchiveRejectionReason = "gzip_trailing"
	RejectTarHeader            ArchiveRejectionReason = "tar_header"
	RejectTarType              ArchiveRejectionReason = "tar_type"
	RejectTarOrder             ArchiveRejectionReason = "tar_order"
	RejectTarPath              ArchiveRejectionReason = "tar_path"
	RejectTarCollision         ArchiveRejectionReason = "tar_collision"
	RejectTarEnd               ArchiveRejectionReason = "tar_end"
	RejectManifestNoncanonical ArchiveRejectionReason = "manifest_noncanonical"
	RejectManifestMismatch     ArchiveRejectionReason = "manifest_mismatch"
	RejectLimits               ArchiveRejectionReason = "limits"
	RejectUnsupportedFormat    ArchiveRejectionReason = "unsupported_format"
)

const maxCanonicalTarBytes = ArchiveMaxTotalBytes + ArchiveMaxFileBytes + (ArchiveMaxFiles+1)*2*ustarBlockSize + 2*ustarBlockSize

type ArchiveRejectionError struct {
	Reason ArchiveRejectionReason
	detail string
}

func (rejection *ArchiveRejectionError) Error() string {
	return "Agent Pack archive rejected (" + string(rejection.Reason) + "): " + rejection.detail
}

func (rejection *ArchiveRejectionError) Unwrap() error { return ErrArchiveInvalid }

func newArchiveRejection(reason ArchiveRejectionReason, detail string) error {
	return &ArchiveRejectionError{Reason: reason, detail: detail}
}

func ArchiveRejectionReasonOf(err error) (ArchiveRejectionReason, bool) {
	var rejection *ArchiveRejectionError
	if !errors.As(err, &rejection) {
		return "", false
	}
	return rejection.Reason, true
}

type CanonicalArchive struct {
	Digest   string
	Manifest ArchiveManifest
	Files    map[string][]byte
	Names    []string
}

func ReadCanonicalArchive(in io.Reader) (*CanonicalArchive, error) {
	if in == nil {
		return nil, fmt.Errorf("Agent Pack archive is required")
	}
	raw, err := io.ReadAll(io.LimitReader(in, ArchiveMaxCompressedBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read Agent Pack archive: %w", err)
	}
	return ParseCanonicalArchive(raw)
}

func ParseCanonicalArchive(raw []byte) (*CanonicalArchive, error) {
	if int64(len(raw)) > ArchiveMaxCompressedBytes {
		return nil, newArchiveRejection(RejectLimits, "compressed archive exceeds the size limit")
	}
	tape, err := inflateCanonicalGzip(raw)
	if err != nil {
		return nil, err
	}
	manifestBytes, files, names, err := parseCanonicalUSTAR(tape)
	if err != nil {
		return nil, err
	}
	manifest, err := ParseCanonicalManifest(manifestBytes)
	if err != nil {
		return nil, err
	}
	for _, name := range names {
		expected, listed := manifest.Files[name]
		sum := sha256.Sum256(files[name])
		if !listed || expected != hex.EncodeToString(sum[:]) {
			return nil, newArchiveRejection(RejectManifestMismatch, "archive file is not listed with its digest")
		}
	}
	if len(manifest.Files) != len(names) {
		return nil, newArchiveRejection(RejectManifestMismatch, "manifest lists a file the archive does not contain")
	}
	sum := sha256.Sum256(raw)
	return &CanonicalArchive{Digest: hex.EncodeToString(sum[:]), Manifest: manifest, Files: files, Names: names}, nil
}

func inflateCanonicalGzip(raw []byte) ([]byte, error) {
	if len(raw) < 10 || raw[0] != 0x1f || raw[1] != 0x8b || raw[2] != 8 || raw[3] != 0 ||
		raw[4] != 0 || raw[5] != 0 || raw[6] != 0 || raw[7] != 0 || (raw[8] != 0 && raw[8] != 2 && raw[8] != 4) {
		return nil, newArchiveRejection(RejectGzipHeader, "gzip header is not canonical")
	}
	body := bytes.NewReader(raw[10:])
	inflater := flate.NewReader(body)
	tape, err := io.ReadAll(io.LimitReader(inflater, maxCanonicalTarBytes+1))
	if len(tape) > maxCanonicalTarBytes {
		return nil, newArchiveRejection(RejectLimits, "decompressed archive exceeds the size limit")
	}
	if err != nil {
		return nil, newArchiveRejection(RejectGzipHeader, "deflate stream is invalid")
	}
	rest := raw[len(raw)-body.Len():]
	if len(rest) < 8 {
		return nil, newArchiveRejection(RejectGzipTrailing, "gzip trailer is truncated")
	}
	if binary.LittleEndian.Uint32(rest[0:4]) != crc32.ChecksumIEEE(tape) || int64(binary.LittleEndian.Uint32(rest[4:8])) != int64(len(tape))%(1<<32) {
		return nil, newArchiveRejection(RejectGzipTrailing, "gzip trailer does not match the content")
	}
	rest = rest[8:]
	if len(rest) >= 2 && rest[0] == 0x1f && rest[1] == 0x8b {
		return nil, newArchiveRejection(RejectGzipMembers, "archive has more than one gzip member")
	}
	if len(rest) > 0 {
		return nil, newArchiveRejection(RejectGzipTrailing, "archive has trailing bytes")
	}
	return tape, nil
}

func parseCanonicalUSTAR(tape []byte) ([]byte, map[string][]byte, []string, error) {
	files := map[string][]byte{}
	names := []string{}
	var manifestBytes []byte
	var total int64
	offset := 0
	for index := 0; ; index++ {
		if len(tape)-offset < ustarBlockSize {
			return nil, nil, nil, newArchiveRejection(RejectTarEnd, "tar stream ends inside a header")
		}
		block := tape[offset : offset+ustarBlockSize]
		if isZeroBlock(block) {
			if len(tape)-offset != 2*ustarBlockSize || !isZeroBlock(tape[offset+ustarBlockSize:]) {
				return nil, nil, nil, newArchiveRejection(RejectTarEnd, "tar stream does not end with exactly two zero blocks")
			}
			break
		}
		var header [ustarBlockSize]byte
		copy(header[:], block)
		checksum, ok := parseUSTAROctal(header[148:156])
		if !ok || checksum != ustarChecksum(header) {
			return nil, nil, nil, newArchiveRejection(RejectTarHeader, "tar header checksum is invalid")
		}
		name := cString(header[0:100])
		if index == 0 && (name == "pack.yaml" || name == "signature") {
			return nil, nil, nil, newArchiveRejection(RejectUnsupportedFormat, "archive uses the retired format 1 layout")
		}
		if string(header[257:263]) != "ustar\x00" || string(header[263:265]) != "00" {
			return nil, nil, nil, newArchiveRejection(RejectTarHeader, "tar header is not USTAR")
		}
		if header[156] != '0' {
			return nil, nil, nil, newArchiveRejection(RejectTarType, "tar entry is not a regular file")
		}
		if prefix := cString(header[345:500]); prefix != "" {
			name = prefix + "/" + name
		}
		if index == 0 {
			if name != ArchiveManifestName {
				return nil, nil, nil, newArchiveRejection(RejectTarOrder, "manifest is not the first entry")
			}
		} else {
			if !validArchivePath(name) {
				return nil, nil, nil, newArchiveRejection(RejectTarPath, "tar entry path is invalid")
			}
			if len(names) > 0 && name <= names[len(names)-1] {
				return nil, nil, nil, newArchiveRejection(RejectTarOrder, "tar entries are not in ascending byte order")
			}
		}
		size, ok := parseUSTAROctal(header[124:136])
		if !ok {
			return nil, nil, nil, newArchiveRejection(RejectTarHeader, "tar entry size is invalid")
		}
		if index > ArchiveMaxFiles {
			return nil, nil, nil, newArchiveRejection(RejectLimits, "archive exceeds the file count limit")
		}
		if size > ArchiveMaxFileBytes {
			return nil, nil, nil, newArchiveRejection(RejectLimits, "tar entry exceeds the file size limit")
		}
		if index > 0 {
			total += size
			if total > ArchiveMaxTotalBytes {
				return nil, nil, nil, newArchiveRejection(RejectLimits, "archive exceeds the total size limit")
			}
		}
		expected, err := encodeUSTARHeader(name, size)
		if err != nil || expected != header {
			return nil, nil, nil, newArchiveRejection(RejectTarHeader, "tar header is not canonical")
		}
		offset += ustarBlockSize
		padded := int((size + ustarBlockSize - 1) / ustarBlockSize * ustarBlockSize)
		if len(tape)-offset < padded {
			return nil, nil, nil, newArchiveRejection(RejectTarEnd, "tar stream ends inside an entry")
		}
		data := tape[offset : offset+int(size)]
		if !isZeroBlock(tape[offset+int(size) : offset+padded]) {
			return nil, nil, nil, newArchiveRejection(RejectTarHeader, "tar entry padding is not zero")
		}
		offset += padded
		if index == 0 {
			manifestBytes = data
			continue
		}
		files[name] = data
		names = append(names, name)
	}
	if manifestBytes == nil {
		return nil, nil, nil, newArchiveRejection(RejectTarOrder, "archive has no manifest entry")
	}
	if archivePathCollision(append([]string{ArchiveManifestName}, names...)) != "" {
		return nil, nil, nil, newArchiveRejection(RejectTarCollision, "archive paths collide")
	}
	return manifestBytes, files, names, nil
}

func parseUSTAROctal(field []byte) (int64, bool) {
	var value int64
	digits := 0
	index := 0
	for ; index < len(field) && field[index] >= '0' && field[index] <= '7'; index++ {
		value = value*8 + int64(field[index]-'0')
		digits++
	}
	if digits == 0 || digits > 11 {
		return 0, false
	}
	for ; index < len(field); index++ {
		if field[index] != 0 && field[index] != ' ' {
			return 0, false
		}
	}
	return value, true
}

func cString(field []byte) string {
	if end := bytes.IndexByte(field, 0); end >= 0 {
		return string(field[:end])
	}
	return string(field)
}

func isZeroBlock(block []byte) bool {
	for _, value := range block {
		if value != 0 {
			return false
		}
	}
	return true
}
