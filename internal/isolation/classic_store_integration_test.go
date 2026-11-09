package isolation

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const storeProbeLabel = "our-runner-classic-probe"

func runStoreProbeDocker(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Env = dockerEnvironment()
	output, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(output)), err
}

func storeProbeUsesContainerd(t *testing.T, ctx context.Context) bool {
	t.Helper()
	status, err := runStoreProbeDocker(ctx, "info", "--format", "{{json .DriverStatus}}")
	if err != nil {
		t.Fatalf("docker info: %v: %s", err, status)
	}
	return strings.Contains(status, "io.containerd.snapshotter.v1")
}

func buildStoreProbeImage(t *testing.T, ctx context.Context) (string, string) {
	t.Helper()
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatal(err)
	}
	unique := hex.EncodeToString(suffix)
	tag := storeProbeLabel + ":" + unique
	label := storeProbeLabel + "=" + unique
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "Dockerfile"), "FROM scratch\nCOPY probe.txt /probe.txt\n", 0o600)
	writeFile(t, filepath.Join(dir, "probe.txt"), unique+"\n", 0o600)
	iidFile := filepath.Join(t.TempDir(), "image-id")
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if output, err := runStoreProbeDocker(cleanupCtx, "image", "rm", "--", tag); err != nil && !strings.Contains(output, "No such image") {
			t.Errorf("remove %s: %v: %s", tag, err, output)
		}
		remaining, err := runStoreProbeDocker(cleanupCtx, "image", "ls", "--quiet", "--filter", "label="+label)
		if err != nil {
			t.Errorf("list labelled images: %v: %s", err, remaining)
		} else if remaining != "" {
			t.Errorf("images labelled %s remain: %s", label, remaining)
		}
	})
	if output, err := runStoreProbeDocker(ctx, "build", "--network", "none", "--label", label, "--tag", tag, "--iidfile", iidFile, dir); err != nil {
		t.Fatalf("docker build: %v: %s", err, output)
	}
	raw, err := os.ReadFile(iidFile)
	if err != nil {
		t.Fatal(err)
	}
	id := strings.TrimSpace(string(raw))
	if !inspectedImageIDPattern.MatchString(id) {
		t.Fatalf("image ID = %q", id)
	}
	return tag, id
}

func TestImageStoreResolvesALocallyBuiltImageByDigest(t *testing.T) {
	t.Run("classic store", func(t *testing.T) {
		if os.Getenv("RUN_DOCKER_CLASSIC_STORE_PROBE") != "1" {
			t.Skip("set RUN_DOCKER_CLASSIC_STORE_PROBE=1 and CLASSIC_DOCKER_HOST for the classic image store gate")
		}
		host := os.Getenv("CLASSIC_DOCKER_HOST")
		if host == "" {
			t.Fatal("CLASSIC_DOCKER_HOST is required")
		}
		t.Setenv("DOCKER_HOST", host)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		if storeProbeUsesContainerd(t, ctx) {
			t.Fatalf("%s uses the containerd image store; set CLASSIC_DOCKER_HOST to a classic store daemon", host)
		}
		tag, id := buildStoreProbeImage(t, ctx)
		pinned := tag + "@" + id
		_, err := InspectImage(ctx, pinned)
		if !errors.Is(err, ErrImageResolvesOnlyByID) {
			t.Fatalf("InspectImage(%s) = %v, want ErrImageResolvesOnlyByID", pinned, err)
		}
		t.Logf("%s does not resolve: %v", pinned, err)
		got, err := runStoreProbeDocker(ctx, "image", "inspect", "--format", "{{.Id}}", "--", id)
		if err != nil {
			t.Fatalf("inspect %s: %v: %s", id, err, got)
		}
		if got != id {
			t.Fatalf("inspect %s = %q", id, got)
		}
		t.Logf("%s resolves to %s", id, got)
	})
	t.Run("containerd store", func(t *testing.T) {
		if os.Getenv("RUN_DOCKER_SANDBOX_PROBE") != "1" {
			t.Skip("set RUN_DOCKER_SANDBOX_PROBE=1 for the container integration gate")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		if !storeProbeUsesContainerd(t, ctx) {
			t.Fatal("DOCKER_HOST does not use the containerd image store; set it to the rootless daemon")
		}
		tag, id := buildStoreProbeImage(t, ctx)
		pinned := tag + "@" + id
		got, err := InspectImage(ctx, pinned)
		if err != nil {
			t.Fatalf("InspectImage(%s) = %v", pinned, err)
		}
		if got != id {
			t.Fatalf("InspectImage(%s) = %q, want %q", pinned, got, id)
		}
		t.Logf("%s resolves to %s", pinned, got)
	})
}
