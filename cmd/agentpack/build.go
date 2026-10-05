package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/example/app-runner/internal/agentpack"
	"github.com/example/app-runner/internal/safeio"
)

const buildProvider = "claude-code"

type buildOutput struct {
	Digest string `json:"digest"`
	Bytes  int64  `json:"bytes"`
	Files  int    `json:"files"`
}

func runBuild(args []string, stdout io.Writer, stderr io.Writer) error {
	flags := newFlagSet("build", stderr)
	source := flags.String("source", "", "pack source directory")
	packKey := flags.String("pack-key", "", "pack key")
	version := flags.String("version", "", "stable semantic version")
	provider := flags.String("provider", "", "provider (claude-code)")
	out := flags.String("out", "", "archive output path")
	force := flags.Bool("force", false, "replace an existing output file")
	if err := parseFlags(flags, args, 0); err != nil {
		return err
	}
	if err := requireFlags(map[string]string{"source": *source, "pack-key": *packKey, "version": *version, "provider": *provider, "out": *out}); err != nil {
		return err
	}
	if *provider != buildProvider {
		return fmt.Errorf("provider %q is not supported by this builder; use %s", *provider, buildProvider)
	}
	if _, err := os.Lstat(*out); err == nil && !*force {
		return fmt.Errorf("output %q already exists; pass --force to replace it", *out)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("output %q cannot be inspected", *out)
	}
	var archive bytes.Buffer
	summary, err := agentpack.BuildArchiveFromDirectory(*source, agentpack.ArchiveBuildOptions{
		PackKey: *packKey, Provider: *provider, Version: *version,
	}, &archive)
	if err != nil {
		return err
	}
	if err := publishArchive(*out, archive.Bytes(), *force); err != nil {
		return err
	}
	return writeJSON(stdout, buildOutput{Digest: summary.Digest, Bytes: summary.Bytes, Files: summary.Files})
}

func publishArchive(out string, archive []byte, force bool) error {
	directory := filepath.Dir(out)
	temporary, err := os.CreateTemp(directory, ".agentpack-build-*")
	if err != nil {
		return fmt.Errorf("output directory %q is not writable", directory)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o644); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("output %q mode cannot be set", out)
	}
	if _, err := temporary.Write(archive); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("output %q cannot be written", out)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("output %q cannot be synced", out)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("output %q cannot be closed", out)
	}
	if force {
		if info, err := os.Lstat(out); err == nil && !info.Mode().IsRegular() {
			return fmt.Errorf("output %q is not a regular file", out)
		}
		if err := os.Rename(temporaryPath, out); err != nil {
			return fmt.Errorf("output %q cannot be replaced", out)
		}
	} else if err := os.Link(temporaryPath, out); err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("output %q already exists; pass --force to replace it", out)
		}
		return fmt.Errorf("output %q cannot be published", out)
	}
	if err := safeio.SyncDirectory(directory); err != nil {
		return fmt.Errorf("output directory %q cannot be synced", directory)
	}
	return nil
}
