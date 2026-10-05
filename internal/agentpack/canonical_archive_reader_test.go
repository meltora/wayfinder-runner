package agentpack

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

var updateConformanceCorpus = flag.Bool("update", false, "regenerate the Agent Pack conformance corpus")

const conformanceDirectory = "testdata/conformance"

type conformanceCase struct {
	Name        string            `json:"name"`
	File        string            `json:"file"`
	Description string            `json:"description"`
	Valid       bool              `json:"valid"`
	Reason      string            `json:"reason,omitempty"`
	Digest      string            `json:"digest,omitempty"`
	PackKey     string            `json:"packKey,omitempty"`
	Provider    string            `json:"provider,omitempty"`
	Version     string            `json:"version,omitempty"`
	Files       map[string]string `json:"files,omitempty"`
}

type conformanceFile struct {
	Format int               `json:"format"`
	Cases  []conformanceCase `json:"cases"`
}

type tapeEntry struct {
	header [ustarBlockSize]byte
	data   []byte
}

type generatedCase struct {
	meta    conformanceCase
	archive []byte
}

func splitGoldenTape(t *testing.T, tape []byte) []tapeEntry {
	t.Helper()
	entries := []tapeEntry{}
	offset := 0
	for !isZeroBlock(tape[offset : offset+ustarBlockSize]) {
		var entry tapeEntry
		copy(entry.header[:], tape[offset:offset+ustarBlockSize])
		size, ok := parseUSTAROctal(entry.header[124:136])
		if !ok {
			t.Fatal("golden tape has an invalid size")
		}
		offset += ustarBlockSize
		entry.data = append([]byte(nil), tape[offset:offset+int(size)]...)
		offset += int((size + ustarBlockSize - 1) / ustarBlockSize * ustarBlockSize)
		entries = append(entries, entry)
	}
	return entries
}

func cloneEntries(entries []tapeEntry) []tapeEntry {
	out := make([]tapeEntry, len(entries))
	for index, entry := range entries {
		out[index] = tapeEntry{header: entry.header, data: append([]byte(nil), entry.data...)}
	}
	return out
}

func fixHeaderChecksum(header *[ustarBlockSize]byte) {
	copy(header[148:156], fmt.Sprintf("%06o\x00 ", ustarChecksum(*header)))
}

func canonicalTapeEntry(t *testing.T, name string, data []byte) tapeEntry {
	t.Helper()
	header, err := encodeUSTARHeader(name, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	return tapeEntry{header: header, data: data}
}

func rawSizedEntry(t *testing.T, name string, size int, data []byte) tapeEntry {
	t.Helper()
	header, err := encodeUSTARHeader(name, 0)
	if err != nil {
		t.Fatal(err)
	}
	copy(header[124:136], fmt.Sprintf("%011o\x00", size))
	fixHeaderChecksum(&header)
	return tapeEntry{header: header, data: data}
}

func renameEntry(entry *tapeEntry, name string) {
	for index := 0; index < 100; index++ {
		entry.header[index] = 0
	}
	for index := 345; index < 500; index++ {
		entry.header[index] = 0
	}
	copy(entry.header[0:100], name)
	fixHeaderChecksum(&entry.header)
}

func mutateHeader(entry *tapeEntry, mutate func(header *[ustarBlockSize]byte)) {
	mutate(&entry.header)
	fixHeaderChecksum(&entry.header)
}

func tapeBytes(entries []tapeEntry, zeroBlocks int) []byte {
	var out bytes.Buffer
	for _, entry := range entries {
		out.Write(entry.header[:])
		out.Write(entry.data)
		if remainder := len(entry.data) % ustarBlockSize; remainder != 0 {
			out.Write(make([]byte, ustarBlockSize-remainder))
		}
	}
	out.Write(make([]byte, zeroBlocks*ustarBlockSize))
	return out.Bytes()
}

func gzipBytes(t *testing.T, tape []byte, level int) []byte {
	t.Helper()
	var out bytes.Buffer
	writer, err := gzip.NewWriterLevel(&out, level)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(tape); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func withManifest(t *testing.T, entries []tapeEntry, manifest []byte) []tapeEntry {
	t.Helper()
	out := cloneEntries(entries)
	out[0] = canonicalTapeEntry(t, ArchiveManifestName, manifest)
	return out
}

func generateConformanceCorpus(t *testing.T) []generatedCase {
	t.Helper()
	golden, summary := buildGoldenArchive(t, goldenConformanceSource)
	parsed, err := ParseCanonicalArchive(golden)
	if err != nil {
		t.Fatal(err)
	}
	tape, err := inflateCanonicalGzip(golden)
	if err != nil {
		t.Fatal(err)
	}
	entries := splitGoldenTape(t, tape)
	manifest := string(entries[0].data)
	cases := []generatedCase{}
	valid := func(name, description string, archive []byte) {
		cases = append(cases, generatedCase{archive: archive, meta: conformanceCase{
			Name: name, Description: description, Valid: true, PackKey: summary.Manifest.PackKey,
			Provider: summary.Manifest.Provider, Version: summary.Manifest.Version, Files: parsed.Manifest.Files,
		}})
	}
	invalid := func(name string, reason ArchiveRejectionReason, description string, archive []byte) {
		cases = append(cases, generatedCase{archive: archive, meta: conformanceCase{
			Name: name, Description: description, Reason: string(reason),
		}})
	}
	gzipEdit := func(edit func([]byte) []byte) []byte {
		return edit(append([]byte(nil), golden...))
	}
	tarCase := func(edit func([]tapeEntry) []tapeEntry) []byte {
		return gzipBytes(t, tapeBytes(edit(cloneEntries(entries)), 2), flate.BestCompression)
	}
	headerCase := func(index int, mutate func(header *[ustarBlockSize]byte)) []byte {
		return tarCase(func(e []tapeEntry) []tapeEntry {
			mutateHeader(&e[index], mutate)
			return e
		})
	}
	renameCase := func(index int, name string) []byte {
		return tarCase(func(e []tapeEntry) []tapeEntry {
			renameEntry(&e[index], name)
			return e
		})
	}
	manifestCase := func(edit func(string) string) []byte {
		return gzipBytes(t, tapeBytes(withManifest(t, entries, []byte(edit(manifest))), 2), flate.BestCompression)
	}
	withFiles := func(extra map[string][]byte) map[string][]byte {
		files := map[string][]byte{}
		for name, data := range parsed.Files {
			files[name] = data
		}
		for name, data := range extra {
			files[name] = data
		}
		return files
	}
	validWithFiles := func(name, description string, extra map[string][]byte) {
		var out bytes.Buffer
		if _, err := WriteCanonicalArchive(&out, ArchiveContent{
			PackKey: summary.Manifest.PackKey, Provider: summary.Manifest.Provider, Version: summary.Manifest.Version, Files: withFiles(extra),
		}); err != nil {
			t.Fatal(err)
		}
		withExtra, err := ParseCanonicalArchive(out.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		cases = append(cases, generatedCase{archive: out.Bytes(), meta: conformanceCase{
			Name: name, Description: description, Valid: true, PackKey: summary.Manifest.PackKey,
			Provider: summary.Manifest.Provider, Version: summary.Manifest.Version, Files: withExtra.Manifest.Files,
		}})
	}
	listedArchive := func(extra map[string][]byte) []byte {
		files := withFiles(extra)
		names := make([]string, 0, len(files))
		for name := range files {
			names = append(names, name)
		}
		sort.Strings(names)
		listed := make([]string, 0, len(names))
		tarEntries := []tapeEntry{{}}
		for _, name := range names {
			sum := sha256.Sum256(files[name])
			listed = append(listed, `"`+name+`":"`+hex.EncodeToString(sum[:])+`"`)
			tarEntries = append(tarEntries, canonicalTapeEntry(t, name, files[name]))
		}
		manifestBytes := `{"files":{` + strings.Join(listed, ",") + `},"format":2,"packKey":"fixture","provider":"claude-code","version":"1.0.0"}`
		tarEntries[0] = canonicalTapeEntry(t, ArchiveManifestName, []byte(manifestBytes))
		return gzipBytes(t, tapeBytes(tarEntries, 2), flate.BestCompression)
	}
	deflateErrorAfter := func(size int) []byte {
		var body bytes.Buffer
		writer, err := flate.NewWriter(&body, flate.BestSpeed)
		if err != nil {
			t.Fatal(err)
		}
		zeros := make([]byte, size)
		if _, err := writer.Write(zeros); err != nil {
			t.Fatal(err)
		}
		if err := writer.Flush(); err != nil {
			t.Fatal(err)
		}
		body.WriteByte(0x07)
		trailer := make([]byte, 8)
		binary.LittleEndian.PutUint32(trailer[0:4], crc32.ChecksumIEEE(zeros))
		binary.LittleEndian.PutUint32(trailer[4:8], uint32(size))
		out := append([]byte{0x1f, 0x8b, 8, 0, 0, 0, 0, 0, 4, 255}, body.Bytes()...)
		return append(out, trailer...)
	}
	firstFile := entries[1]
	firstName := cString(firstFile.header[0:100])

	valid("golden", "golden archive built from testdata/conformance/source", golden)
	valid("valid_xfl_fastest", "same tar stream compressed at the fastest level (XFL=4)", gzipBytes(t, tape, flate.BestSpeed))
	valid("valid_xfl_default", "same tar stream compressed at the default level (XFL=0)", gzipBytes(t, tape, flate.DefaultCompression))
	valid("valid_os_unix", "golden archive with gzip OS=3", gzipEdit(func(b []byte) []byte { b[9] = 3; return b }))
	validWithFiles("valid_path_constructor", "object member names as file names", map[string][]byte{
		"constructor": []byte("c\n"), "hasOwnProperty": []byte("h\n"), "toString": []byte("t\n"),
	})
	validWithFiles("valid_path_prototype", "prototype and constructor as directory and nested names", map[string][]byte{
		"prototype/constructor.md": []byte("p\n"), "skills/valueOf/__proto__x.md": []byte("v\n"),
	})
	validWithFiles("valid_path_resembles_reserved", "names that only resemble reserved ones after ASCII case folding", map[string][]byte{
		".GITIGNORE": []byte("g\n"), ".github/workflow.yml": []byte("w\n"), "AGENT-PACK.JSON.md": []byte("m\n"),
		"__PROTO__x.md": []byte("p\n"), "a.DS_Store": []byte("d\n"), "x._y": []byte("x\n"),
	})

	invalid("gzip_header_magic", RejectGzipHeader, "ID1 is not 0x1f", gzipEdit(func(b []byte) []byte { b[0] = 0x1e; return b }))
	invalid("gzip_header_method", RejectGzipHeader, "CM is not deflate", gzipEdit(func(b []byte) []byte { b[2] = 7; return b }))
	invalid("gzip_header_flags_name", RejectGzipHeader, "FLG has FNAME with a stored name", gzipEdit(func(b []byte) []byte {
		b[3] = 0x08
		return append(append(append([]byte(nil), b[:10]...), 'x', 0), b[10:]...)
	}))
	invalid("gzip_header_flags_text", RejectGzipHeader, "FLG has FTEXT", gzipEdit(func(b []byte) []byte { b[3] = 0x01; return b }))
	invalid("gzip_header_mtime", RejectGzipHeader, "MTIME is not zero", gzipEdit(func(b []byte) []byte { b[4] = 1; return b }))
	invalid("gzip_header_xfl", RejectGzipHeader, "XFL is not 0, 2 or 4", gzipEdit(func(b []byte) []byte { b[8] = 1; return b }))
	invalid("gzip_header_truncated", RejectGzipHeader, "fewer than 10 bytes", golden[:9])
	invalid("gzip_deflate_reserved_block", RejectGzipHeader, "deflate block type 3", gzipEdit(func(b []byte) []byte { b[10] |= 0x06; return b }))
	invalid("gzip_deflate_truncated", RejectGzipHeader, "deflate stream cut in the middle", golden[:len(golden)/2])
	invalid("gzip_crc_mismatch", RejectGzipTrailing, "CRC32 does not match", gzipEdit(func(b []byte) []byte { b[len(b)-8] ^= 0xff; return b }))
	invalid("gzip_isize_mismatch", RejectGzipTrailing, "ISIZE does not match", gzipEdit(func(b []byte) []byte { b[len(b)-4] ^= 0x01; return b }))
	invalid("gzip_trailer_truncated", RejectGzipTrailing, "trailer shorter than 8 bytes", golden[:len(golden)-4])
	invalid("gzip_trailing_garbage", RejectGzipTrailing, "bytes after the trailer", append(append([]byte(nil), golden...), []byte("hidden payload")...))
	invalid("gzip_trailing_zero_padding", RejectGzipTrailing, "zero padding after the trailer", append(append([]byte(nil), golden...), make([]byte, 512)...))
	invalid("gzip_header_deflate_error_at_cap", RejectGzipHeader, "reserved deflate block after exactly the decompressed cap",
		deflateErrorAfter(maxCanonicalTarBytes))
	invalid("gzip_members_two", RejectGzipMembers, "a second gzip member follows", append(append([]byte(nil), golden...), gzipBytes(t, []byte("more"), flate.BestCompression)...))
	invalid("gzip_members_empty_first", RejectGzipMembers, "an empty first member precedes the archive", append(gzipBytes(t, nil, flate.BestCompression), golden...))

	invalid("tar_header_checksum", RejectTarHeader, "header checksum is wrong", tarCase(func(e []tapeEntry) []tapeEntry {
		e[1].header[148] = '7'
		return e
	}))
	invalid("tar_header_magic_gnu", RejectTarHeader, "GNU magic", headerCase(1, func(h *[ustarBlockSize]byte) { copy(h[257:265], "ustar  \x00") }))
	invalid("tar_header_magic_v7", RejectTarHeader, "no magic (v7 header)", headerCase(1, func(h *[ustarBlockSize]byte) { copy(h[257:265], make([]byte, 8)) }))
	invalid("tar_header_version", RejectTarHeader, "USTAR version is not 00", headerCase(1, func(h *[ustarBlockSize]byte) { copy(h[263:265], "01") }))
	invalid("tar_header_mode_0644", RejectTarHeader, "mode is 0644", headerCase(1, func(h *[ustarBlockSize]byte) { copy(h[100:108], "0000644\x00") }))
	invalid("tar_header_mode_setuid", RejectTarHeader, "mode has set-user-ID", headerCase(1, func(h *[ustarBlockSize]byte) { copy(h[100:108], "0004444\x00") }))
	invalid("tar_header_uid", RejectTarHeader, "uid is not zero", headerCase(1, func(h *[ustarBlockSize]byte) { copy(h[108:116], "0001750\x00") }))
	invalid("tar_header_gid", RejectTarHeader, "gid is not zero", headerCase(1, func(h *[ustarBlockSize]byte) { copy(h[116:124], "0000024\x00") }))
	invalid("tar_header_mtime", RejectTarHeader, "mtime is not zero", headerCase(1, func(h *[ustarBlockSize]byte) { copy(h[136:148], "14000000000\x00") }))
	invalid("tar_header_uname", RejectTarHeader, "uname is set", headerCase(1, func(h *[ustarBlockSize]byte) { copy(h[265:297], "root") }))
	invalid("tar_header_gname", RejectTarHeader, "gname is set", headerCase(1, func(h *[ustarBlockSize]byte) { copy(h[297:329], "wheel") }))
	invalid("tar_header_devmajor", RejectTarHeader, "devmajor is not zero", headerCase(1, func(h *[ustarBlockSize]byte) { copy(h[329:337], "0000001\x00") }))
	invalid("tar_header_linkname", RejectTarHeader, "linkname is set", headerCase(1, func(h *[ustarBlockSize]byte) { copy(h[157:257], "target") }))
	invalid("tar_header_name_garbage", RejectTarHeader, "bytes after the name terminator", headerCase(1, func(h *[ustarBlockSize]byte) { h[99] = 'z' }))
	invalid("tar_header_pad_garbage", RejectTarHeader, "header padding bytes are set", headerCase(1, func(h *[ustarBlockSize]byte) { h[511] = 1 }))
	invalid("tar_header_size_spaces", RejectTarHeader, "size field padded with spaces", headerCase(1, func(h *[ustarBlockSize]byte) {
		size, _ := parseUSTAROctal(h[124:136])
		copy(h[124:136], fmt.Sprintf("%o", size)+strings.Repeat(" ", 11-len(fmt.Sprintf("%o", size)))+"\x00")
	}))
	invalid("tar_header_split_noncanonical", RejectTarHeader, "short path stored with a prefix", tarCase(func(e []tapeEntry) []tapeEntry {
		renameEntry(&e[1], "")
		copy(e[1].header[0:100], strings.SplitN(firstName, "/", 2)[1])
		copy(e[1].header[345:500], strings.SplitN(firstName, "/", 2)[0])
		fixHeaderChecksum(&e[1].header)
		return e
	}))
	invalid("tar_header_data_padding", RejectTarHeader, "entry data padding is not zero", func() []byte {
		raw := tapeBytes(cloneEntries(entries), 2)
		raw[ustarBlockSize+len(entries[0].data)] = 'x'
		return gzipBytes(t, raw, flate.BestCompression)
	}())

	invalid("tar_type_rega", RejectTarType, "typeflag NUL (old regular file)", headerCase(1, func(h *[ustarBlockSize]byte) { h[156] = 0 }))
	invalid("tar_type_hardlink", RejectTarType, "typeflag 1", headerCase(1, func(h *[ustarBlockSize]byte) { h[156] = '1' }))
	invalid("tar_type_symlink", RejectTarType, "typeflag 2", headerCase(1, func(h *[ustarBlockSize]byte) { h[156] = '2' }))
	invalid("tar_type_directory", RejectTarType, "typeflag 5", headerCase(1, func(h *[ustarBlockSize]byte) { h[156] = '5' }))
	invalid("tar_type_fifo", RejectTarType, "typeflag 6", headerCase(1, func(h *[ustarBlockSize]byte) { h[156] = '6' }))
	invalid("tar_type_pax", RejectTarType, "PAX extended header", headerCase(1, func(h *[ustarBlockSize]byte) { h[156] = 'x' }))
	invalid("tar_type_pax_global", RejectTarType, "PAX global header", headerCase(1, func(h *[ustarBlockSize]byte) { h[156] = 'g' }))
	invalid("tar_type_gnu_longname", RejectTarType, "GNU long name", headerCase(1, func(h *[ustarBlockSize]byte) { h[156] = 'L' }))
	invalid("tar_type_manifest", RejectTarType, "manifest entry is a directory", headerCase(0, func(h *[ustarBlockSize]byte) { h[156] = '5' }))

	invalid("tar_path_absolute", RejectTarPath, "leading slash", renameCase(1, "/"+firstName))
	invalid("tar_path_dotdot", RejectTarPath, "parent segment", renameCase(1, "../escape.md"))
	invalid("tar_path_dot", RejectTarPath, "current-directory segment", renameCase(1, "./"+firstName))
	invalid("tar_path_empty_segment", RejectTarPath, "empty segment", renameCase(1, ".claude-plugin//plugin.json"))
	invalid("tar_path_trailing_slash", RejectTarPath, "trailing slash", renameCase(1, ".claude-plugin/"))
	invalid("tar_path_space", RejectTarPath, "space outside the charset", renameCase(1, ".claude plugin.json"))
	invalid("tar_path_non_ascii", RejectTarPath, "non-ASCII byte", renameCase(1, ".caf\xc3\xa9.json"))
	invalid("tar_path_backslash", RejectTarPath, "backslash", renameCase(1, ".a\\b"))
	invalid("tar_path_depth", RejectTarPath, "16 slashes", renameCase(1, ".d/"+strings.Repeat("a/", 15)+"z"))
	invalid("tar_path_reserved_signature", RejectTarPath, "legacy signature as a file", tarCase(func(e []tapeEntry) []tapeEntry {
		return append(e, canonicalTapeEntry(t, "signature", []byte("x")))
	}))
	invalid("tar_path_reserved_manifest", RejectTarPath, "a second manifest entry", tarCase(func(e []tapeEntry) []tapeEntry {
		return append(e, canonicalTapeEntry(t, ArchiveManifestName, e[0].data))
	}))

	invalid("tar_path_proto_file", RejectTarPath, "a listed file named __proto__", listedArchive(map[string][]byte{"__proto__": []byte("proto\n")}))
	invalid("tar_path_proto_dir", RejectTarPath, "a listed file under a __proto__ directory", listedArchive(map[string][]byte{"__proto__/x.md": []byte("x\n")}))
	invalid("tar_path_nested_manifest", RejectTarPath, "a listed nested agent-pack.json", listedArchive(map[string][]byte{"skills/agent-pack.json": []byte("{}")}))
	invalid("tar_path_folded_nested_manifest", RejectTarPath, "a listed sub/AGENT-PACK.JSON (ASCII case fold)", listedArchive(map[string][]byte{"sub/AGENT-PACK.JSON": []byte("{}")}))
	invalid("tar_path_folded_root_manifest", RejectTarPath, "a listed root AGENT-PACK.JSON (ASCII case fold)", listedArchive(map[string][]byte{"AGENT-PACK.JSON": []byte("{}")}))
	invalid("tar_path_folded_proto_file", RejectTarPath, "a listed file named __PROTO__ (ASCII case fold)", listedArchive(map[string][]byte{"__PROTO__": []byte("proto\n")}))
	invalid("tar_path_folded_proto_dir", RejectTarPath, "a listed file under a __Proto__ directory (ASCII case fold)", listedArchive(map[string][]byte{"skills/__Proto__/x.md": []byte("x\n")}))
	invalid("tar_path_git_dir", RejectTarPath, "a listed file under a .git directory", listedArchive(map[string][]byte{".git/config": []byte("x\n")}))
	invalid("tar_path_folded_git_dir", RejectTarPath, "a listed file under a .GIT directory (ASCII case fold)", listedArchive(map[string][]byte{".GIT/config": []byte("x\n")}))
	invalid("tar_path_os_metadata_ds_store", RejectTarPath, "a listed .DS_Store", listedArchive(map[string][]byte{"skills/.DS_Store": []byte("x\n")}))
	invalid("tar_path_folded_os_metadata_ds_store", RejectTarPath, "a listed .ds_store (ASCII case fold)", listedArchive(map[string][]byte{".ds_store": []byte("x\n")}))
	invalid("tar_path_folded_os_metadata_thumbs", RejectTarPath, "a listed THUMBS.DB (ASCII case fold)", listedArchive(map[string][]byte{"THUMBS.DB": []byte("x\n")}))
	invalid("tar_path_os_metadata_apple_double", RejectTarPath, "a listed ._ AppleDouble file", listedArchive(map[string][]byte{"skills/._SKILL.md": []byte("x\n")}))

	invalid("tar_order_manifest_not_first", RejectTarOrder, "a file precedes the manifest", tarCase(func(e []tapeEntry) []tapeEntry {
		e[0], e[1] = e[1], e[0]
		return e
	}))
	invalid("tar_order_descending", RejectTarOrder, "files are not ascending", tarCase(func(e []tapeEntry) []tapeEntry {
		e[1], e[2] = e[2], e[1]
		return e
	}))
	invalid("tar_order_duplicate", RejectTarOrder, "a file appears twice", tarCase(func(e []tapeEntry) []tapeEntry {
		return append(e[:2], append([]tapeEntry{e[1]}, e[2:]...)...)
	}))
	invalid("tar_order_empty", RejectTarOrder, "only the end-of-archive blocks", gzipBytes(t, make([]byte, 2*ustarBlockSize), flate.BestCompression))

	invalid("tar_collision_case", RejectTarCollision, "two paths differ only in case", tarCase(func(e []tapeEntry) []tapeEntry {
		return append(e, canonicalTapeEntry(t, "workflow.md", []byte("x")))
	}))
	invalid("tar_collision_file_directory", RejectTarCollision, "a path is both a file and a directory", tarCase(func(e []tapeEntry) []tapeEntry {
		return append(e, canonicalTapeEntry(t, "skills/job-context/SKILL.md/nested", []byte("x")))
	}))
	invalid("tar_collision_manifest_case", RejectTarPath, "a file folds onto the manifest name, a reserved name under ASCII case folding", tarCase(func(e []tapeEntry) []tapeEntry {
		return append([]tapeEntry{e[0], e[1], canonicalTapeEntry(t, "Agent-Pack.json", []byte("x"))}, e[2:]...)
	}))

	invalid("tar_end_missing", RejectTarEnd, "no end-of-archive blocks", gzipBytes(t, tapeBytes(cloneEntries(entries), 0), flate.BestCompression))
	invalid("tar_end_one_block", RejectTarEnd, "one end-of-archive block", gzipBytes(t, tapeBytes(cloneEntries(entries), 1), flate.BestCompression))
	invalid("tar_end_three_blocks", RejectTarEnd, "three end-of-archive blocks", gzipBytes(t, tapeBytes(cloneEntries(entries), 3), flate.BestCompression))
	invalid("tar_end_trailing_data", RejectTarEnd, "data after the end-of-archive blocks", gzipBytes(t, append(tapeBytes(cloneEntries(entries), 2), 'x'), flate.BestCompression))
	invalid("tar_end_truncated_header", RejectTarEnd, "stream ends inside a header", gzipBytes(t, tapeBytes(cloneEntries(entries), 2)[:ustarBlockSize/2], flate.BestCompression))
	invalid("tar_end_truncated_data", RejectTarEnd, "stream ends inside entry data", gzipBytes(t, tapeBytes(cloneEntries(entries), 2)[:ustarBlockSize+10], flate.BestCompression))

	invalid("unsupported_format_v1_layout", RejectUnsupportedFormat, "format 1 layout with pack.yaml first", renameCase(0, "pack.yaml"))
	invalid("unsupported_format_v1_signature", RejectUnsupportedFormat, "format 1 layout with signature first", renameCase(0, "signature"))
	invalid("unsupported_format_manifest_3", RejectUnsupportedFormat, "manifest format 3", manifestCase(func(m string) string {
		return strings.Replace(m, `"format":2`, `"format":3`, 1)
	}))

	invalid("manifest_nesting_deep_format3", RejectManifestNoncanonical, "format 3 with nesting deeper than the JSON decoder limit", manifestCase(func(m string) string {
		return strings.Replace(m, `"format":2`, `"format":3,"nested":`+strings.Repeat("[", 10001)+strings.Repeat("]", 10001), 1)
	}))
	invalid("manifest_nesting_over_bound", RejectManifestNoncanonical, "nesting one level over the manifest bound", manifestCase(func(m string) string {
		return strings.Replace(m, `"format":2`, `"format":3,"nested":`+strings.Repeat("[", archiveManifestMaxNesting)+strings.Repeat("]", archiveManifestMaxNesting), 1)
	}))
	invalid("manifest_whitespace", RejectManifestNoncanonical, "space after a colon", manifestCase(func(m string) string {
		return strings.Replace(m, `"format":2`, `"format": 2`, 1)
	}))
	invalid("manifest_trailing_newline", RejectManifestNoncanonical, "trailing newline", manifestCase(func(m string) string { return m + "\n" }))
	invalid("manifest_bom", RejectManifestNoncanonical, "UTF-8 byte order mark", manifestCase(func(m string) string { return "\xef\xbb\xbf" + m }))
	invalid("manifest_key_order", RejectManifestNoncanonical, "keys not sorted", manifestCase(func(m string) string {
		return strings.Replace(m, `"packKey":"fixture","provider":"claude-code"`, `"provider":"claude-code","packKey":"fixture"`, 1)
	}))
	invalid("manifest_file_order", RejectManifestNoncanonical, "file keys not sorted", manifestCase(func(m string) string {
		var files map[string]string
		var whole map[string]json.RawMessage
		_ = json.Unmarshal([]byte(m), &whole)
		_ = json.Unmarshal(whole["files"], &files)
		first := `".claude-plugin/plugin.json":"` + files[".claude-plugin/plugin.json"] + `",`
		return strings.Replace(strings.Replace(m, first, "", 1), `}`, `,`+strings.TrimSuffix(first, ",")+`}`, 1)
	}))
	invalid("manifest_unknown_key", RejectManifestNoncanonical, "unknown key", manifestCase(func(m string) string {
		return strings.Replace(m, `"version"`, `"signature":"x","version"`, 1)
	}))
	invalid("manifest_duplicate_key", RejectManifestNoncanonical, "duplicate key", manifestCase(func(m string) string {
		return strings.Replace(m, `"format":2`, `"format":2,"format":2`, 1)
	}))
	invalid("manifest_escaped_key", RejectManifestNoncanonical, "escaped key spelling", manifestCase(func(m string) string {
		return strings.Replace(m, `"version"`, `"vers`+"\\"+`u0069on"`, 1)
	}))
	invalid("manifest_uppercase_hex", RejectManifestNoncanonical, "uppercase digest hex", manifestCase(func(m string) string {
		digest := parsed.Manifest.Files["WORKFLOW.md"]
		return strings.Replace(m, digest, strings.ToUpper(digest), 1)
	}))
	invalid("manifest_provider_unknown", RejectManifestNoncanonical, "provider outside the reader set", manifestCase(func(m string) string {
		return strings.Replace(m, `"claude-code"`, `"claude"`, 1)
	}))
	invalid("manifest_version_prerelease", RejectManifestNoncanonical, "prerelease version", manifestCase(func(m string) string {
		return strings.Replace(m, `"1.0.0"`, `"1.0.0-rc.1"`, 1)
	}))
	invalid("manifest_pack_key_invalid", RejectManifestNoncanonical, "pack key is not kebab-case", manifestCase(func(m string) string {
		return strings.Replace(m, `"fixture"`, `"Fixture"`, 1)
	}))
	invalid("manifest_format_string", RejectManifestNoncanonical, "format is a string", manifestCase(func(m string) string {
		return strings.Replace(m, `"format":2`, `"format":"2"`, 1)
	}))
	invalid("manifest_format_missing", RejectManifestNoncanonical, "format key missing", manifestCase(func(m string) string {
		return strings.Replace(m, `"format":2,`, ``, 1)
	}))
	invalid("manifest_empty_files", RejectManifestNoncanonical, "files object is empty", manifestCase(func(m string) string {
		return m[:strings.Index(m, `{"files":{`)+len(`{"files":{`)] + m[strings.Index(m, `},"format"`):]
	}))
	invalid("manifest_not_json", RejectManifestNoncanonical, "manifest is not JSON", manifestCase(func(string) string { return "files: {}" }))
	invalid("manifest_invalid_utf8", RejectManifestNoncanonical, "manifest has invalid UTF-8", manifestCase(func(m string) string {
		return strings.Replace(m, `"fixture"`, "\"fixt\xffre\"", 1)
	}))

	invalid("manifest_missing_file", RejectManifestMismatch, "manifest lists a file the archive lacks", tarCase(func(e []tapeEntry) []tapeEntry {
		return append(e[:1], e[2:]...)
	}))
	invalid("manifest_extra_file", RejectManifestMismatch, "archive holds an unlisted file", tarCase(func(e []tapeEntry) []tapeEntry {
		return append(e, canonicalTapeEntry(t, "zz-unlisted.md", []byte("x")))
	}))
	invalid("manifest_digest_mismatch", RejectManifestMismatch, "file bytes differ from the listed digest", tarCase(func(e []tapeEntry) []tapeEntry {
		e[1].data[0] ^= 0x01
		return e
	}))

	invalid("limits_decompression_bomb", RejectLimits, "inflates beyond the decompressed cap", gzipBytes(t, make([]byte, maxCanonicalTarBytes+1), flate.BestSpeed))
	invalid("limits_deflate_error_after_cap", RejectLimits, "reserved deflate block one byte after the decompressed cap",
		deflateErrorAfter(maxCanonicalTarBytes+1))
	invalid("limits_deflate_error_within_chunk", RejectLimits, "reserved deflate block 4096 bytes after the decompressed cap",
		deflateErrorAfter(maxCanonicalTarBytes+4096))
	invalid("limits_file_size", RejectLimits, "entry larger than 1 MiB", gzipBytes(t, tapeBytes(append(cloneEntries(entries),
		rawSizedEntry(t, "zz-big.bin", ArchiveMaxFileBytes+1, make([]byte, ArchiveMaxFileBytes+1))), 2), flate.BestSpeed))
	invalid("limits_manifest_size", RejectLimits, "manifest larger than 1 MiB", gzipBytes(t, tapeBytes([]tapeEntry{
		rawSizedEntry(t, ArchiveManifestName, ArchiveMaxFileBytes+1, make([]byte, ArchiveMaxFileBytes+1))}, 2), flate.BestSpeed))
	invalid("limits_file_count", RejectLimits, "1025 files", tarCase(func(e []tapeEntry) []tapeEntry {
		out := []tapeEntry{e[0]}
		for index := 0; index <= ArchiveMaxFiles; index++ {
			out = append(out, canonicalTapeEntry(t, fmt.Sprintf("f%04d", index), nil))
		}
		return out
	}))
	invalid("limits_total_size", RejectLimits, "files total more than 64 MiB", gzipBytes(t, tapeBytes(func() []tapeEntry {
		out := []tapeEntry{entries[0]}
		for index := 0; index <= ArchiveMaxTotalBytes/ArchiveMaxFileBytes; index++ {
			out = append(out, canonicalTapeEntry(t, fmt.Sprintf("f%04d", index), make([]byte, ArchiveMaxFileBytes)))
		}
		return out
	}(), 2), flate.BestSpeed))

	for index := range cases {
		cases[index].meta.File = cases[index].meta.Name + ".tgz"
		if cases[index].meta.Valid {
			cases[index].meta.Digest = digestHex(cases[index].archive)
		}
	}
	return cases
}

func digestHex(raw []byte) string {
	parsed, err := ParseCanonicalArchive(raw)
	if err != nil {
		return ""
	}
	return parsed.Digest
}

func TestConformanceCorpusMatchesGeneratorAndReader(t *testing.T) {
	generated := generateConformanceCorpus(t)
	file := conformanceFile{Format: 1}
	for _, generatedCase := range generated {
		file.Cases = append(file.Cases, generatedCase.meta)
	}
	encoded, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	if *updateConformanceCorpus {
		for _, generatedCase := range generated {
			if err := os.WriteFile(filepath.Join(conformanceDirectory, generatedCase.meta.File), generatedCase.archive, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(conformanceDirectory, "cases.json"), encoded, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	committed, err := os.ReadFile(filepath.Join(conformanceDirectory, "cases.json"))
	if err != nil {
		t.Fatalf("conformance corpus missing; run go test -run TestConformanceCorpus -update: %v", err)
	}
	if !bytes.Equal(committed, encoded) {
		t.Fatal("committed cases.json differs from the generator")
	}
	invalidCount := 0
	reasons := map[string]bool{}
	for _, generatedCase := range generated {
		meta := generatedCase.meta
		t.Run(meta.Name, func(t *testing.T) {
			archive, err := os.ReadFile(filepath.Join(conformanceDirectory, meta.File))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(archive, generatedCase.archive) {
				t.Fatal("committed archive differs from the generator")
			}
			parsed, err := ParseCanonicalArchive(archive)
			if meta.Valid {
				if err != nil {
					t.Fatalf("valid case rejected: %v", err)
				}
				if parsed.Digest != meta.Digest || parsed.Manifest.PackKey != meta.PackKey || len(parsed.Files) != len(meta.Files) {
					t.Fatalf("parsed = %+v", parsed)
				}
				return
			}
			reason, ok := ArchiveRejectionReasonOf(err)
			if !ok || string(reason) != meta.Reason {
				t.Fatalf("reason = %q %v (%v), want %q", reason, ok, err, meta.Reason)
			}
			if !errors.Is(err, ErrArchiveInvalid) {
				t.Fatal("rejection does not wrap ErrArchiveInvalid")
			}
		})
		if !meta.Valid {
			invalidCount++
			reasons[meta.Reason] = true
		}
	}
	if invalidCount < 30 || len(reasons) != 13 {
		t.Fatalf("corpus has %d invalid cases over %d reasons", invalidCount, len(reasons))
	}
	if generated[0].meta.Digest != goldenConformanceDigest {
		t.Fatalf("golden case digest = %s", generated[0].meta.Digest)
	}
}

func TestParseCanonicalArchiveRejectsOversizeCompressedInput(t *testing.T) {
	_, err := ReadCanonicalArchive(bytes.NewReader(make([]byte, ArchiveMaxCompressedBytes+1)))
	if reason, ok := ArchiveRejectionReasonOf(err); !ok || reason != RejectLimits {
		t.Fatalf("reason = %q %v", reason, ok)
	}
	if _, err := ReadCanonicalArchive(nil); err == nil {
		t.Fatal("nil reader accepted")
	}
	if _, err := ReadCanonicalArchive(failingReader{}); err == nil {
		t.Fatal("reader failure ignored")
	}
	if _, ok := ArchiveRejectionReasonOf(errors.New("other")); ok {
		t.Fatal("foreign error classified")
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }

func TestParseUSTAROctalRejectsMalformedFields(t *testing.T) {
	for _, field := range []string{"", "\x00\x00", "12a\x00", "123456789012", " 12\x00"} {
		if _, ok := parseUSTAROctal([]byte(field)); ok {
			t.Errorf("field %q accepted", field)
		}
	}
	if value, ok := parseUSTAROctal([]byte("0000017\x00")); !ok || value != 15 {
		t.Fatalf("value = %d %v", value, ok)
	}
}
