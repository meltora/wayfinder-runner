package agentpack

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

func MountAuthenticatedArchive(archive io.Reader, destination string) (*MountedPack, error) {
	return MountAuthenticatedArchiveWithPolicy(archive, destination, MountPolicy{})
}

type MountPolicy struct {
	Digest   string
	Version  string
	Provider string
}

type MountedPack struct {
	Digest   string
	Manifest ArchiveManifest
	Root     string
}

func MountAuthenticatedArchiveWithPolicy(archive io.Reader, destination string, policy MountPolicy) (*MountedPack, error) {
	if archive == nil {
		return nil, fmt.Errorf("Agent Pack archive is required")
	}
	parsed, err := ReadCanonicalArchive(archive)
	if err != nil {
		return nil, err
	}
	if policy.Digest != "" && parsed.Digest != policy.Digest {
		return nil, fmt.Errorf("Agent Pack archive digest does not match signed reference: %w", ErrArchiveDigestMismatch)
	}
	if policy.Version != "" && parsed.Manifest.Version != policy.Version {
		return nil, fmt.Errorf("Agent Pack version does not match signed reference")
	}
	if policy.Provider != "" && canonicalProvider(parsed.Manifest.Provider) != canonicalProvider(policy.Provider) {
		return nil, fmt.Errorf("Agent Pack manifest provider does not match signed authority")
	}
	if err := mountAuthenticatedFiles(destination, parsed.Files); err != nil {
		return nil, err
	}
	return &MountedPack{Digest: parsed.Digest, Manifest: parsed.Manifest, Root: destination}, nil
}

func mountAuthenticatedFiles(destination string, files map[string][]byte) (err error) {
	if err := prepareDestination(destination); err != nil {
		return fmt.Errorf("prepare Agent Pack job view: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(destination)
		}
	}()
	root, err := os.OpenRoot(destination)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()

	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	directories := map[string]bool{".": true}
	for _, relative := range names {
		name := filepath.FromSlash(relative)
		directory := filepath.Dir(name)
		if err := root.MkdirAll(directory, 0o700); err != nil {
			return err
		}
		for current := directory; current != "."; current = filepath.Dir(current) {
			directories[current] = true
		}
		file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o444)
		if err != nil {
			return err
		}
		if _, err := file.Write(files[relative]); err != nil {
			_ = file.Close()
			return err
		}
		if err := file.Sync(); err != nil {
			_ = file.Close()
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
	}
	dirs := make([]string, 0, len(directories))
	for directory := range directories {
		dirs = append(dirs, directory)
	}
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
	for _, directory := range dirs {
		if err := root.Chmod(directory, 0o555); err != nil {
			return err
		}
	}
	committed = true
	return nil
}
