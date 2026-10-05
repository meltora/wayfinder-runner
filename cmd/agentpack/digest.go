package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/example/app-runner/internal/agentpack"
)

type digestOutput struct {
	Digest string `json:"digest"`
}

func runDigest(args []string, stdout io.Writer, stderr io.Writer) error {
	flags := newFlagSet("digest", stderr)
	if err := parseFlags(flags, args, 1); err != nil {
		return err
	}
	archive, err := readArchiveFile(flags.Arg(0))
	if err != nil {
		return err
	}
	return writeJSON(stdout, digestOutput{Digest: archive.Digest})
}

func readArchiveFile(path string) (*agentpack.CanonicalArchive, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("archive %q must be a regular file", path)
	}
	file, err := os.Open(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("archive %q cannot be opened", path)
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, fmt.Errorf("archive %q changed while it was opened", path)
	}
	return agentpack.ReadCanonicalArchive(file)
}
