package daemon

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/journal"
)

type provenanceArtifact struct {
	name        string
	contentType string
	content     string
}

func uploadedArtifacts(t *testing.T, jr *jobRunner) []provenanceArtifact {
	t.Helper()
	records, err := jr.d.journal.Unacked(context.Background(), journal.KindUpload, 100)
	if err != nil {
		t.Fatal(err)
	}
	var artifacts []provenanceArtifact
	for _, record := range records {
		var pending pendingArtifactUpload
		if err := json.Unmarshal(record.Payload, &pending); err != nil {
			t.Fatal(err)
		}
		content, err := os.ReadFile(filepath.Join(jr.d.collectors.Root, filepath.FromSlash(pending.Path)))
		if err != nil {
			t.Fatal(err)
		}
		artifacts = append(artifacts, provenanceArtifact{name: pending.Artifact.Name, contentType: pending.Artifact.ContentType, content: string(content)})
	}
	return artifacts
}

type orderedEvidence struct {
	mu      sync.Mutex
	entries []string
}

func recordEvidenceOrder(t *testing.T, f agentResultTest) *orderedEvidence {
	t.Helper()
	order := &orderedEvidence{}
	sink := f.jr.d.eventSink
	f.jr.d.eventSink = func(ctx context.Context, payload any) error {
		if event, ok := payload.(map[string]any); ok && event["kind"] == "test_results" {
			var names []string
			for _, artifact := range uploadedArtifacts(t, f.jr) {
				names = append(names, artifact.name)
			}
			order.mu.Lock()
			order.entries = append(order.entries, fmt.Sprintf("event:%v artifacts:%s", event["check_id"], strings.Join(names, ",")))
			order.mu.Unlock()
		}
		return sink(ctx, payload)
	}
	return order
}

func gitBlobSHA(content string) string {
	sum := sha1.Sum([]byte(fmt.Sprintf("blob %d\x00%s", len(content), content)))
	return hex.EncodeToString(sum[:])
}

var durationPattern = regexp.MustCompile(`"duration_ms":[0-9]+`)

func normalizedProvenance(content string) string {
	return durationPattern.ReplaceAllString(content, `"duration_ms":0`)
}

func fixtureTreeDigest() string {
	content := "module.exports = 1\n"
	contentSum := sha256.Sum256([]byte(content))
	records := "node_modules/fixture-dep\x00dir\x000\x000\x00\n" +
		fmt.Sprintf("node_modules/fixture-dep/index.js\x00file\x000\x00%d\x00%s\n", len(content), hex.EncodeToString(contentSum[:]))
	sum := sha256.Sum256([]byte(records))
	return hex.EncodeToString(sum[:])
}

const directArgvJSON = `["npm","ci","--ignore-scripts","--no-audit","--no-fund","--no-update-notifier"]`

func runTerminalChecks(t *testing.T, f agentResultTest, checks map[string]config.CheckConfig, order ...string) string {
	t.Helper()
	commit := f.publish(t)
	f.jr.job.RequiredChecks = order
	f.jr.d.cfg.Checks = checks
	f.jr.runRequiredChecks(context.Background(), map[string]string{"repo-1": commit}, nil)
	return commit
}

func TestProvenanceArtifactHasTheExactDocument(t *testing.T) {
	for _, tc := range []struct {
		name  string
		seed  func(t *testing.T, origin string)
		check config.CheckConfig
		want  func(commit string) string
	}{
		{"no install", nil, shellCheck("true"), func(commit string) string {
			return `{"schema":"our-runner.check-install.v1","job_id":"j1","attempt_id":"a1","check_id":"lint","repository_id":"repo-1",` +
				`"commit_sha":"` + commit + `","isolation":"direct","hermetic":false,"carried_directories":[],"install":null,"result":"passed"}`
		}},
		{"installed", func(t *testing.T, origin string) { seedNPMProject(t, origin) }, npmInstallCheck("exit 1"), func(commit string) string {
			return `{"schema":"our-runner.check-install.v1","job_id":"j1","attempt_id":"a1","check_id":"lint","repository_id":"repo-1",` +
				`"commit_sha":"` + commit + `","isolation":"direct","hermetic":false,"carried_directories":[],` +
				`"install":{"ecosystem":"npm","network":"host","offline":false,"tool_version":"10.0.0-fake","argv":` + directArgvJSON + `,` +
				`"directories":[{"directory":".","lockfile":"package-lock.json","lockfile_blob_sha":"` + gitBlobSHA(fixtureLockfile) + `",` +
				`"package_json_blob_sha":"` + gitBlobSHA(fixturePackageJSON) + `","result":"installed","duration_ms":0,` +
				`"entries":2,"bytes":19,"tree_digest":"` + fixtureTreeDigest() + `"}]},"result":"failed"}`
		}},
		{"refused", func(t *testing.T, origin string) {
			seedNPMProject(t, origin)
			writeAgentFile(t, origin, "web/package.json", fixturePackageJSON)
		}, npmInstallCheck("true", ".", "web", "api"), func(commit string) string {
			return `{"schema":"our-runner.check-install.v1","job_id":"j1","attempt_id":"a1","check_id":"lint","repository_id":"repo-1",` +
				`"commit_sha":"` + commit + `","isolation":"direct","hermetic":false,"carried_directories":[],` +
				`"install":{"ecosystem":"npm","network":"host","offline":false,"tool_version":null,"argv":` + directArgvJSON + `,"directories":[` +
				`{"directory":".","lockfile":"package-lock.json","lockfile_blob_sha":"` + gitBlobSHA(fixtureLockfile) + `",` +
				`"package_json_blob_sha":"` + gitBlobSHA(fixturePackageJSON) + `","result":"not_run","duration_ms":0,"entries":null,"bytes":null,"tree_digest":null},` +
				`{"directory":"web","lockfile":null,"lockfile_blob_sha":null,"package_json_blob_sha":null,"result":"refused","duration_ms":0,"entries":null,"bytes":null,"tree_digest":null},` +
				`{"directory":"api","lockfile":null,"lockfile_blob_sha":null,"package_json_blob_sha":null,"result":"not_run","duration_ms":0,"entries":null,"bytes":null,"tree_digest":null}` +
				`]},"result":"error"}`
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var seed func(origin string)
			if tc.seed != nil {
				seed = func(origin string) { tc.seed(t, origin) }
			}
			f := newAgentResultTestWith(t, seed)
			useFakeNPM(t, fakeNPMInstallBody)

			commit := runTerminalChecks(t, f, map[string]config.CheckConfig{"lint": tc.check}, "lint")

			artifacts := uploadedArtifacts(t, f.jr)
			if len(artifacts) != 1 || artifacts[0].name != "runner-check-install-lint.json" || artifacts[0].contentType != "application/json" {
				t.Fatalf("artifacts = %+v", artifacts)
			}
			if got, want := normalizedProvenance(artifacts[0].content), tc.want(commit); got != want {
				t.Fatalf("provenance =\n%s\nwant\n%s", got, want)
			}
			for _, leak := range []string{f.jr.d.cfg.StateDir, f.jr.workDir, "PATH="} {
				if strings.Contains(artifacts[0].content, leak) {
					t.Fatalf("provenance leaks %q", leak)
				}
			}
		})
	}
}

func TestDockerProvenanceIsHermetic(t *testing.T) {
	jr, _ := testJobRunner(t)
	jr.d.cfg.Isolation, jr.d.cfg.JobNetwork = "docker", "none"
	document, err := jr.checkProvenanceDocument("lint", "repo-1", strings.Repeat("a", 40), checkReport{outcome: checkOutcome{Result: "passed"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(document), `"isolation":"docker","hermetic":true,"carried_directories":[],"install":null,`) {
		t.Fatalf("docker provenance = %s", document)
	}
}

func TestProvenanceIsPublishedBeforeEachTerminalEvent(t *testing.T) {
	f := newNPMResultTest(t)
	useFakeNPM(t, fakeNPMInstallBody)
	order := recordEvidenceOrder(t, f)

	runTerminalChecks(t, f, map[string]config.CheckConfig{"a": shellCheck("true"), "b": npmInstallCheck("true")}, "a", "b")

	want := []string{
		"event:a artifacts:runner-check-install-a.json",
		"event:b artifacts:runner-check-install-a.json,runner-check-install-b.json",
	}
	if !slices.Equal(order.entries, want) {
		t.Fatalf("order = %q", order.entries)
	}
}

func TestProvenanceCardinalityFollowsTheRequiredChecks(t *testing.T) {
	for _, count := range []int{0, 1, 3} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			f := newAgentResultTest(t)
			checks := map[string]config.CheckConfig{}
			var order []string
			for index := range count {
				id := fmt.Sprintf("check-%d", index)
				checks[id] = shellCheck("true")
				order = append(order, id)
			}

			runTerminalChecks(t, f, checks, order...)

			var names []string
			for _, artifact := range uploadedArtifacts(t, f.jr) {
				names = append(names, artifact.name)
			}
			var want []string
			for _, id := range order {
				want = append(want, "runner-check-install-"+id+".json")
			}
			if !slices.Equal(names, want) {
				t.Fatalf("artifacts = %q, want %q", names, want)
			}
		})
	}
}

func TestNoProvenanceForIterativeUnregisteredOrForeignChecks(t *testing.T) {
	f := newNPMResultTest(t)
	useFakeNPM(t, fakeNPMInstallBody)
	assertCheckResult(t, f.runCheck(t, npmInstallCheck("true")), "passed")
	if artifacts := uploadedArtifacts(t, f.jr); len(artifacts) != 0 {
		t.Fatalf("iterative run published %+v", artifacts)
	}

	foreign := shellCheck("true")
	foreign.RepositoryID = "not-in-job"
	runTerminalChecks(t, f, map[string]config.CheckConfig{"foreign": foreign}, "unregistered", "foreign")

	if artifacts := uploadedArtifacts(t, f.jr); len(artifacts) != 0 {
		t.Fatalf("terminal run published %+v", artifacts)
	}
	if events := f.events.byKind("test_results"); len(events) != 3 {
		t.Fatalf("events = %v", events)
	}
}

func TestAFailedProvenanceSaveStillSendsTheEvent(t *testing.T) {
	f := newAgentResultTest(t)
	blocker := filepath.Join(trustedResultsRoot(f.jr.d.cfg.StateDir, f.jr.workDir), checkProvenanceDirectory)
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	runTerminalChecks(t, f, map[string]config.CheckConfig{"lint": shellCheck("true")}, "lint")

	if artifacts := uploadedArtifacts(t, f.jr); len(artifacts) != 0 {
		t.Fatalf("artifacts = %+v", artifacts)
	}
	events := f.events.byKind("test_results")
	if len(events) != 1 || events[0]["result"] != "passed" {
		t.Fatalf("events = %v", events)
	}
}

func TestTheAgentCannotPublishARunnerReservedArtifactName(t *testing.T) {
	jr, _ := testJobRunner(t)
	if err := os.WriteFile(filepath.Join(jr.workDir, "report.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	for index, name := range []string{"runner-x", "Runner-x", "RUNNER-x", "runner-check-install-go-test.json"} {
		_, err := call(t, jr, "platform_publish_artifact", fmt.Sprintf(`{"name":%q,"path":"report.json","idempotency_key":"reserved-%d"}`, name, index))
		if err == nil || err.Error() != "artifact names starting with runner- are reserved for the runner" {
			t.Fatalf("name %q: err = %v", name, err)
		}
	}
	if artifacts := uploadedArtifacts(t, jr); len(artifacts) != 0 {
		t.Fatalf("reserved names were stored: %+v", artifacts)
	}
	if _, err := call(t, jr, "platform_publish_artifact", `{"name":"my-runner-x","path":"report.json","idempotency_key":"allowed"}`); err != nil {
		t.Fatalf("my-runner-x refused: %v", err)
	}
	if artifacts := uploadedArtifacts(t, jr); len(artifacts) != 1 || artifacts[0].name != "my-runner-x" {
		t.Fatalf("artifacts = %+v", artifacts)
	}
}
