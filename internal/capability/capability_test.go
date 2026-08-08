package capability

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"runtime"
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/isolation"
)

func fakeDetector() *Detector {
	d := New("r-1", "0.1.0", map[string]string{"pool": "test"})
	d.LookPath = func(name string) (string, error) {
		if name == "git" || name == "codex" || name == "opencode" || name == "tmux" {
			return "/usr/bin/" + name, nil
		}
		return "", errors.New("not found")
	}
	d.RunVersion = func(ctx context.Context, name string, args ...string) (string, error) {
		return name + " version 1.0", nil
	}
	return d
}

func TestCollect(t *testing.T) {
	m := fakeDetector().Collect(context.Background())
	if m.OS != runtime.GOOS || m.Arch != runtime.GOARCH {
		t.Fatalf("os/arch mismatch")
	}
	if m.GitVersion != "git version 1.0" {
		t.Fatalf("GitVersion = %q", m.GitVersion)
	}
	if m.ContainerEngine != "" {
		t.Fatalf("ContainerEngine = %q, want empty", m.ContainerEngine)
	}
	var codex *v1.AgentCapability
	for i := range m.Agents {
		if m.Agents[i].Provider == "codex" {
			codex = &m.Agents[i]
		}
	}
	if codex == nil || !codex.Available || codex.Version != "codex version 1.0" {
		t.Fatalf("codex capability = %+v", codex)
	}
	var openCode *v1.AgentCapability
	for i := range m.Agents {
		if m.Agents[i].Provider == "opencode" {
			openCode = &m.Agents[i]
		}
	}
	if openCode == nil || !openCode.Available || openCode.Version != "opencode version 1.0" {
		t.Fatalf("OpenCode capability = %+v", openCode)
	}
	if len(m.Supervisors) != 1 || m.Supervisors[0] != "tmux" {
		t.Fatalf("Supervisors = %v", m.Supervisors)
	}
	if m.Labels["pool"] != "test" {
		t.Fatalf("labels not propagated")
	}
}

func TestCollectReportsRootless(t *testing.T) {
	d := fakeDetector()
	d.LookPath = func(name string) (string, error) {
		if name == "docker" {
			return "/usr/bin/docker", nil
		}
		return "", errors.New("not found")
	}
	d.ProbeDocker = func(ctx context.Context) isolation.DockerInfo {
		return isolation.DockerInfo{Available: true, Rootless: true, ServerVersion: "26.1.0"}
	}
	m := d.Collect(context.Background())
	if m.ContainerEngine != "docker" || !m.ContainerRootless {
		t.Fatalf("engine = %q rootless = %v", m.ContainerEngine, m.ContainerRootless)
	}

	d.ProbeDocker = func(ctx context.Context) isolation.DockerInfo {
		return isolation.DockerInfo{Available: true, ServerVersion: "26.1.0"}
	}
	m = d.Collect(context.Background())
	if m.ContainerRootless {
		t.Fatal("rootful docker must not report rootless")
	}
}

func TestSignVerify(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	d := fakeDetector()
	m := d.Collect(context.Background())
	if err := d.Sign(&m, key); err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if m.Signature == "" {
		t.Fatal("empty signature")
	}
	sig, _ := base64.StdEncoding.DecodeString(m.Signature)
	m.Signature = ""
	payload, _ := json.Marshal(&m)
	digest := sha256.Sum256(payload)
	if !ecdsa.VerifyASN1(&key.PublicKey, digest[:], sig) {
		t.Fatal("signature does not verify")
	}
}
