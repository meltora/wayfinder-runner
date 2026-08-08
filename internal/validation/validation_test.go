package validation

import (
	"math"
	"strings"
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func TestDisplayNameAllowsOrdinaryCharactersAndRejectsPathSyntax(t *testing.T) {
	for _, name := range []string{"result.txt", "x0-report.json", "release notes.md"} {
		if err := DisplayName("name", name); err != nil {
			t.Errorf("DisplayName(%q): %v", name, err)
		}
	}
	for _, name := range []string{"dir/file", `dir\\file`, "bad\x00name"} {
		if err := DisplayName("name", name); err == nil {
			t.Errorf("DisplayName(%q) unexpectedly succeeded", name)
		}
	}
}

func validJob() v1.JobSpec {
	return v1.JobSpec{
		JobID: "job-1", AttemptID: "attempt-1", Kind: v1.JobKindPlan, FencingToken: 1,
		Repositories: []v1.JobRepository{{RepositoryID: "repo-1", Primary: true}},
		Ticket:       v1.TicketContext{Key: "PROJ-1", Summary: "Implement feature"},
	}
}

func TestJobSpecAcceptsValidPlanAndRejectsEveryUntrustedBoundary(t *testing.T) {
	if err := JobSpec(validJob()); err != nil {
		t.Fatal(err)
	}
	mutations := map[string]func(*v1.JobSpec){
		"job id":            func(j *v1.JobSpec) { j.JobID = "../bad" },
		"attempt id":        func(j *v1.JobSpec) { j.AttemptID = "bad id" },
		"kind":              func(j *v1.JobSpec) { j.Kind = "delete" },
		"zero fence":        func(j *v1.JobSpec) { j.FencingToken = 0 },
		"oversized fence":   func(j *v1.JobSpec) { j.FencingToken = uint64(math.MaxInt64) + 1 },
		"no repository":     func(j *v1.JobSpec) { j.Repositories = nil },
		"bad repository id": func(j *v1.JobSpec) { j.Repositories[0].RepositoryID = "bad/id" },
		"duplicate repo":    func(j *v1.JobSpec) { j.Repositories = append(j.Repositories, j.Repositories[0]) },
		"no primary":        func(j *v1.JobSpec) { j.Repositories[0].Primary = false },
		"multiple primary": func(j *v1.JobSpec) {
			j.Repositories = append(j.Repositories, v1.JobRepository{RepositoryID: "repo-2", Primary: true})
		},
		"unsafe base": func(j *v1.JobSpec) { j.Repositories[0].BaseBranch = "../main" },
		"ticket key":  func(j *v1.JobSpec) { j.Ticket.Key = "" },
		"summary":     func(j *v1.JobSpec) { j.Ticket.Summary = "" },
		"description": func(j *v1.JobSpec) { j.Ticket.Description = strings.Repeat("x", (256<<10)+1) },
		"acceptance":  func(j *v1.JobSpec) { j.Ticket.AcceptanceCriteria = strings.Repeat("x", (128<<10)+1) },
		"review":      func(j *v1.JobSpec) { j.ReviewComment = "bad\x00comment" },
		"title":       func(j *v1.JobSpec) { j.PRTitle = strings.Repeat("x", 513) },
		"revision":    func(j *v1.JobSpec) { j.ApprovedPlanRevision = -1 },
		"unapproved build": func(j *v1.JobSpec) {
			j.Kind = v1.JobKindBuild
			j.ApprovedPlanRevision = 0
		},
		"continuation":    func(j *v1.JobSpec) { j.ContinuationCount = 101 },
		"resume id":       func(j *v1.JobSpec) { j.ResumeSessionID = "bad session" },
		"too many checks": func(j *v1.JobSpec) { j.RequiredChecks = make([]string, 65) },
		"bad check":       func(j *v1.JobSpec) { j.RequiredChecks = []string{"bad/check"} },
		"duplicate check": func(j *v1.JobSpec) { j.RequiredChecks = []string{"unit", "unit"} },
		"pack key": func(j *v1.JobSpec) {
			j.AgentPack = &v1.AgentPackRef{KeyID: "bad key", Version: "1", Digest: strings.Repeat("a", 64), URL: "https://example.test/p"}
		},
		"pack version": func(j *v1.JobSpec) {
			j.AgentPack = &v1.AgentPackRef{KeyID: "key", Version: "", Digest: strings.Repeat("a", 64), URL: "https://example.test/p"}
		},
		"pack digest length": func(j *v1.JobSpec) {
			j.AgentPack = &v1.AgentPackRef{KeyID: "key", Version: "1", Digest: "bad", URL: "https://example.test/p"}
		},
		"pack digest case": func(j *v1.JobSpec) {
			j.AgentPack = &v1.AgentPackRef{KeyID: "key", Version: "1", Digest: strings.Repeat("A", 64), URL: "https://example.test/p"}
		},
		"pack url": func(j *v1.JobSpec) {
			j.AgentPack = &v1.AgentPackRef{KeyID: "key", Version: "1", Digest: strings.Repeat("a", 64)}
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			job := validJob()
			job.Repositories = append([]v1.JobRepository(nil), job.Repositories...)
			mutate(&job)
			if err := JobSpec(job); err == nil {
				t.Fatal("invalid job accepted")
			}
		})
	}
}

func TestOpaqueIdentifiersGitRefsAndWebURLs(t *testing.T) {
	for _, id := range []string{"a", "runner-1", "a.b_c"} {
		if err := ID("id", id); err != nil {
			t.Errorf("ID(%q): %v", id, err)
		}
	}
	for _, id := range []string{"", ".", "..", "-leading", strings.Repeat("x", 129), "bad/id"} {
		if err := ID("id", id); err == nil {
			t.Errorf("ID(%q) accepted", id)
		}
	}
	for _, ref := range []string{"main", "feature/new-flow", "release-1.2"} {
		if err := GitRef("ref", ref); err != nil {
			t.Errorf("GitRef(%q): %v", ref, err)
		}
	}
	for _, ref := range []string{"", "-bad", ".bad", "bad.", "bad/", "a..b", "a@{b", "a//b", "a b", "a/.hidden", "a/x.lock"} {
		if err := GitRef("ref", ref); err == nil {
			t.Errorf("GitRef(%q) accepted", ref)
		}
	}
	if err := WebURL("preview", "https://preview.example.test/path?q=ok"); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"", "http://example.test", "https://user@example.test", "https://example.test/#fragment", "relative"} {
		if err := WebURL("preview", raw); err == nil {
			t.Errorf("WebURL(%q) accepted", raw)
		}
	}
}
