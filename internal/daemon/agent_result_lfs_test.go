package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	lfsTestAttributes = "*.bin filter=lfs diff=lfs merge=lfs -text\n"
	lfsTestPointer    = "version https://git-lfs.github.com/spec/v1\noid sha256:4d7a214614ab2935c943f9e0ff69d22eadbb8f32b1258daaa5e2ca24d17e2393\nsize 12345\n"
)

func newLFSResultTest(t *testing.T) agentResultTest {
	t.Helper()
	return newAgentResultTestWith(t, func(origin string) {
		writeAgentFile(t, origin, ".gitattributes", lfsTestAttributes)
		writeAgentFile(t, origin, "asset.bin", lfsTestPointer)
	})
}

func TestLFSPathsRefuseContentThatIsNotAPointer(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(t *testing.T, f agentResultTest)
		want   string
	}{
		{
			name: "raw content at a tracked LFS path",
			change: func(t *testing.T, f agentResultTest) {
				writeAgentFile(t, f.checkout, "asset.bin", strings.Repeat("binary", 400))
			},
			want: "at asset.bin that .gitattributes stores in Git LFS",
		},
		{
			name: "a new raw file at an LFS path",
			change: func(t *testing.T, f agentResultTest) {
				writeAgentFile(t, f.checkout, "models/new.bin", "raw model\n")
			},
			want: "at models/new.bin that .gitattributes stores in Git LFS",
		},
		{
			name: "an LFS rule the agent added",
			change: func(t *testing.T, f agentResultTest) {
				writeAgentFile(t, f.checkout, ".gitattributes", lfsTestAttributes+"*.dat filter=lfs -text\n")
				writeAgentFile(t, f.checkout, "data.dat", "raw data\n")
			},
			want: "at data.dat that .gitattributes stores in Git LFS",
		},
		{
			name: "several raw files are counted",
			change: func(t *testing.T, f agentResultTest) {
				writeAgentFile(t, f.checkout, "a.bin", "raw\n")
				writeAgentFile(t, f.checkout, "b.bin", "raw\n")
			},
			want: "and 1 more",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newLFSResultTest(t)
			tc.change(t, f)
			assertRefused(t, f, tc.want)
		})
	}
}

func TestLFSPathsPublishPointersAndOtherContent(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(t *testing.T, f agentResultTest)
	}{
		{
			name: "a replacement pointer",
			change: func(t *testing.T, f agentResultTest) {
				writeAgentFile(t, f.checkout, "asset.bin", strings.Replace(lfsTestPointer, "12345", "999", 1))
			},
		},
		{
			name: "an unchanged pointer beside another change",
			change: func(t *testing.T, f agentResultTest) {
				writeAgentFile(t, f.checkout, "feature.txt", "real\n")
			},
		},
		{
			name: "raw content outside LFS paths",
			change: func(t *testing.T, f agentResultTest) {
				writeAgentFile(t, f.checkout, "notes.txt", strings.Repeat("text ", 500))
			},
		},
		{
			name: "a symbolic link at an LFS path",
			change: func(t *testing.T, f agentResultTest) {
				if err := os.Symlink("asset.bin", filepath.Join(f.checkout, "alias.bin")); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "an empty file at an LFS path",
			change: func(t *testing.T, f agentResultTest) {
				writeAgentFile(t, f.checkout, "empty.bin", "")
			},
		},
		{
			name: "a deleted LFS file",
			change: func(t *testing.T, f agentResultTest) {
				removeAgentPath(t, f, "asset.bin")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newLFSResultTest(t)
			tc.change(t, f)
			if sha := f.publish(t); sha == f.repository().StartSHA {
				t.Fatal("the change was not published")
			}
		})
	}
}

func TestLFSRefusalAppliesToEveryResultUse(t *testing.T) {
	f := newLFSResultTest(t)
	writeAgentFile(t, f.checkout, "asset.bin", "raw\n")

	_, err := f.jr.resolveAgentResult(context.Background(), f.repository(), "check run")

	var resultErr *agentResultError
	if !errors.As(err, &resultErr) || !strings.Contains(err.Error(), "stores in Git LFS") {
		t.Fatalf("err = %v, want the LFS refusal", err)
	}
	assertNoHostPath(t, err.Error(), f.jr.d.cfg.StateDir, f.checkout)
}

func TestValidLFSPointer(t *testing.T) {
	oid := "oid sha256:" + strings.Repeat("a", 64) + "\n"
	for _, tc := range []struct {
		name    string
		content string
		want    bool
	}{
		{"the current specification", "version https://git-lfs.github.com/spec/v1\n" + oid + "size 10\n", true},
		{"the legacy specification", "version https://hawser.github.com/spec/v1\n" + oid + "size 0\n", true},
		{"an extension line", "version https://git-lfs.github.com/spec/v1\next-0-foo sha256:" + strings.Repeat("b", 64) + "\n" + oid + "size 10\n", true},
		{"empty content", "", true},
		{"no trailing newline", "version https://git-lfs.github.com/spec/v1\n" + oid + "size 10", false},
		{"an unknown version", "version https://example.test/v9\n" + oid + "size 10\n", false},
		{"a short oid", "version https://git-lfs.github.com/spec/v1\noid sha256:abc\nsize 10\n", false},
		{"an uppercase oid", "version https://git-lfs.github.com/spec/v1\noid sha256:" + strings.Repeat("A", 64) + "\nsize 10\n", false},
		{"a missing size", "version https://git-lfs.github.com/spec/v1\n" + oid, false},
		{"a negative size", "version https://git-lfs.github.com/spec/v1\n" + oid + "size -1\n", false},
		{"keys out of order", "version https://git-lfs.github.com/spec/v1\nsize 10\n" + oid, false},
		{"a duplicate key", "version https://git-lfs.github.com/spec/v1\n" + oid + oid + "size 10\n", false},
		{"carriage returns", "version https://git-lfs.github.com/spec/v1\r\n" + oid + "size 10\n", false},
		{"raw content", "\x89PNG\r\n\x1a\n", false},
		{"too large", "version https://git-lfs.github.com/spec/v1\n" + oid + "size 10\nx-pad " + strings.Repeat("p", 1100) + "\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := validLFSPointer([]byte(tc.content)); got != tc.want {
				t.Fatalf("validLFSPointer = %v, want %v", got, tc.want)
			}
		})
	}
}
