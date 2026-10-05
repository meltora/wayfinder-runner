package daemon

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/config"
)

const agentPackGatewayFixtureVariable = "SHORTSTACK_AGENT_PACK_GATEWAY_FIXTURE"

type agentPackGatewayFixture struct {
	Origin   string                        `json:"origin"`
	CAFile   string                        `json:"caFile"`
	CertFile string                        `json:"certFile"`
	KeyFile  string                        `json:"keyFile"`
	Provider string                        `json:"provider"`
	Items    []agentPackGatewayFixtureItem `json:"items"`
}

type agentPackGatewayFixtureItem struct {
	Name        string `json:"name"`
	Digest      string `json:"digest"`
	Version     string `json:"version"`
	URL         string `json:"url"`
	Expect      string `json:"expect"`
	Calls       int    `json:"calls"`
	MountFile   string `json:"mountFile"`
	MountSHA256 string `json:"mountSha256"`
}

func loadAgentPackGatewayFixture(t *testing.T) agentPackGatewayFixture {
	t.Helper()
	path := os.Getenv(agentPackGatewayFixtureVariable)
	if path == "" {
		t.Skip(agentPackGatewayFixtureVariable + " is not set")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var fixture agentPackGatewayFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	if fixture.Origin == "" || fixture.Provider == "" || len(fixture.Items) == 0 {
		t.Fatal("fixture is incomplete")
	}
	return fixture
}

func agentPackGatewayTLS(t *testing.T, fixture agentPackGatewayFixture) *tls.Config {
	t.Helper()
	certificate, err := tls.LoadX509KeyPair(fixture.CertFile, fixture.KeyFile)
	if err != nil {
		t.Fatalf("load client certificate: %v", err)
	}
	caPEM, err := os.ReadFile(fixture.CAFile)
	if err != nil {
		t.Fatalf("read CA: %v", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		t.Fatal("CA file holds no certificate")
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{certificate},
		RootCAs:      roots,
	}
}

func fileSHA256(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", filepath.Base(path), err)
	}
	sum := sha256.Sum256(contents)
	return hex.EncodeToString(sum[:])
}

func TestAgentPackGatewayMaterialization(t *testing.T) {
	fixture := loadAgentPackGatewayFixture(t)
	tlsConfig := agentPackGatewayTLS(t, fixture)
	for _, item := range fixture.Items {
		t.Run(item.Name, func(t *testing.T) {
			stateDir := t.TempDir()
			daemon := &Daemon{
				cfg: config.Config{
					StateDir:               stateDir,
					Provider:               fixture.Provider,
					ControlPlaneHTTPURL:    fixture.Origin,
					AllowedArtifactOrigins: []string{fixture.Origin},
				},
				uploader: &artifactUploader{},
			}
			daemon.setControlTLS(tlsConfig)
			runner := &jobRunner{d: daemon, ctx: context.Background()}
			reference := v1.AgentPackRef{
				Version: item.Version, Digest: item.Digest, URL: item.URL, KeyID: "pack-key-1",
			}
			calls := item.Calls
			if calls < 1 {
				calls = 1
			}
			for call := 1; call <= calls; call++ {
				destination := filepath.Join(t.TempDir(), fmt.Sprintf("view-%d", call))
				writableCleanupJobView(t, destination)
				err := runner.materializeAgentPack(&reference, destination)
				if item.Expect == "ok" {
					if err != nil {
						t.Fatalf("call %d: materialize: %v", call, err)
					}
					cached := filepath.Join(stateDir, "agent-pack-cache", "archives", item.Digest+".tgz")
					if got := fileSHA256(t, cached); got != item.Digest {
						t.Fatalf("call %d: cached archive sha256 mismatch", call)
					}
					if got := fileSHA256(t, filepath.Join(destination, item.MountFile)); got != item.MountSHA256 {
						t.Fatalf("call %d: mounted file sha256 mismatch", call)
					}
					continue
				}
				category, _, classified := agentPackMaterializationEvidence(err)
				if !classified || string(category) != item.Expect {
					t.Fatalf("call %d: category=%q classified=%v err=%v, want %s", call, category, classified, err, item.Expect)
				}
			}
		})
	}
}
