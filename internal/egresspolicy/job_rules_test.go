package egresspolicy

import (
	"bufio"
	"context"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
)

type fakeJobRulesVerifier struct {
	socketPath string
	requests   chan jobRulesRequest
	releases   chan releaseRequest
}

func startFakeJobRulesVerifier(t *testing.T, respond func(jobRulesRequest) any, release func(releaseRequest) any) *fakeJobRulesVerifier {
	t.Helper()
	temporary, err := os.MkdirTemp("/tmp", "egress-job-rules-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(temporary) })
	verifier := &fakeJobRulesVerifier{
		socketPath: filepath.Join(temporary, "policy.sock"),
		requests:   make(chan jobRulesRequest, 1),
		releases:   make(chan releaseRequest, 1),
	}
	listener, err := net.Listen("unix", verifier.socketPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer connection.Close()
		reader := bufio.NewReaderSize(connection, MaxMessageBytes+2)
		var req jobRulesRequest
		if decodeMessage(reader, &req) != nil {
			return
		}
		verifier.requests <- req
		reply := respond(req)
		if reply == nil || encodeMessage(connection, reply) != nil {
			return
		}
		var rel releaseRequest
		if decodeMessage(reader, &rel) != nil {
			return
		}
		verifier.releases <- rel
		if ack := release(rel); ack != nil {
			_ = encodeMessage(connection, ack)
		}
	}()
	return verifier
}

func heldJobRules(req jobRulesRequest) any {
	return jobRulesResponse{
		SchemaVersion: JobRulesSchemaVersion, Kind: jobRulesResponseKind, Nonce: req.Nonce,
		NetworkID: req.NetworkID, PolicyID: req.PolicyID, ContainerName: req.ContainerName,
		EnforcementID: "nft:runner-egress-7", RulesDigest: strings.Repeat("e", 64),
		ApprovedHostsDigest: req.ApprovedHostsDigest, LeaseID: "job-lease-1",
		Effective: true, UnauthorizedReachabilityBlocked: true, ObservedAt: time.Now().UTC(),
	}
}

func heldThroughRelease(rel releaseRequest) any {
	return releaseResponse{
		SchemaVersion: JobRulesSchemaVersion, Kind: "held_through_release", Nonce: rel.Nonce,
		LeaseID: rel.LeaseID, ReleasedAt: time.Now().UTC(),
	}
}

func validJobRulesRequest() JobRulesRequest {
	return JobRulesRequest{
		NetworkID: strings.Repeat("b", 64), NetworkName: "runner-egress", PolicyID: "policy-v1",
		ContainerName: "our-runner-" + strings.Repeat("0a", 12),
		ApprovedHosts: []string{"*.npmjs.org", "registry.example.com"},
	}
}

func testClient(socketPath string) *Client {
	client := NewClient(socketPath)
	client.ExpectedServerUID = -1
	return client
}

func TestAcquireJobRulesSendsApprovedHostsAndHoldsUntilRelease(t *testing.T) {
	verifier := startFakeJobRulesVerifier(t, heldJobRules, heldThroughRelease)
	lease, rules, err := testClient(verifier.socketPath).AcquireJobRules(context.Background(), validJobRulesRequest())
	if err != nil {
		t.Fatal(err)
	}
	req := <-verifier.requests
	if req.SchemaVersion != 2 || req.Kind != "acquire_job_rules" || req.ContainerName != validJobRulesRequest().ContainerName ||
		!reflect.DeepEqual(req.ApprovedHosts, []string{"*.npmjs.org", "registry.example.com"}) ||
		req.ApprovedHostsDigest != v1.EgressApprovedHostsDigest(req.ApprovedHosts) {
		t.Fatalf("request = %+v", req)
	}
	if rules.EnforcementID != "nft:runner-egress-7" || rules.RulesDigest != strings.Repeat("e", 64) ||
		rules.ApprovedHostsDigest != req.ApprovedHostsDigest || !reflect.DeepEqual(rules.ApprovedHosts, req.ApprovedHosts) {
		t.Fatalf("rules = %+v", rules)
	}
	if !lease.ExpiresAt.IsZero() {
		t.Fatalf("job rules lease must be held for the job, expires at %s", lease.ExpiresAt)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := lease.Complete(ctx); err != nil {
		t.Fatal(err)
	}
	if rel := <-verifier.releases; rel.SchemaVersion != 2 || rel.LeaseID != "job-lease-1" {
		t.Fatalf("release = %+v", rel)
	}
	_ = lease.Close()
}

func TestAcquireJobRulesSendsAnEmptyHostListWhenNothingIsApproved(t *testing.T) {
	verifier := startFakeJobRulesVerifier(t, heldJobRules, heldThroughRelease)
	input := validJobRulesRequest()
	input.ApprovedHosts = nil
	lease, rules, err := testClient(verifier.socketPath).AcquireJobRules(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	req := <-verifier.requests
	if req.ApprovedHosts == nil || len(req.ApprovedHosts) != 0 || rules.ApprovedHosts == nil ||
		rules.ApprovedHostsDigest != v1.EgressApprovedHostsDigest(nil) {
		t.Fatalf("request = %+v rules = %+v", req, rules)
	}
}

func TestAcquireJobRulesRejectsInvalidInputBeforeContactingTheVerifier(t *testing.T) {
	for name, mutate := range map[string]func(*JobRulesRequest){
		"network id":     func(value *JobRulesRequest) { value.NetworkID = "bad" },
		"container":      func(value *JobRulesRequest) { value.ContainerName = "other-container" },
		"policy":         func(value *JobRulesRequest) { value.PolicyID = "" },
		"unsorted hosts": func(value *JobRulesRequest) { value.ApprovedHosts = []string{"b.example.com", "a.example.com"} },
		"ip host":        func(value *JobRulesRequest) { value.ApprovedHosts = []string{"10.0.0.1"} },
	} {
		t.Run(name, func(t *testing.T) {
			input := validJobRulesRequest()
			mutate(&input)
			if _, _, err := testClient(filepath.Join(t.TempDir(), "missing.sock")).AcquireJobRules(context.Background(), input); err == nil ||
				strings.Contains(err.Error(), "inspect egress policy verifier socket") {
				t.Fatalf("invalid job rules request reached the verifier: %v", err)
			}
		})
	}
}

func TestAcquireJobRulesRejectsResponsesThatDoNotBindTheJob(t *testing.T) {
	for name, mutate := range map[string]func(*jobRulesResponse){
		"kind":             func(value *jobRulesResponse) { value.Kind = "lease" },
		"schema":           func(value *jobRulesResponse) { value.SchemaVersion = 1 },
		"nonce":            func(value *jobRulesResponse) { value.Nonce = strings.Repeat("f", 64) },
		"container":        func(value *jobRulesResponse) { value.ContainerName = "our-runner-" + strings.Repeat("ff", 12) },
		"hosts digest":     func(value *jobRulesResponse) { value.ApprovedHostsDigest = v1.EgressApprovedHostsDigest(nil) },
		"not effective":    func(value *jobRulesResponse) { value.Effective = false },
		"reachable":        func(value *jobRulesResponse) { value.UnauthorizedReachabilityBlocked = false },
		"stale":            func(value *jobRulesResponse) { value.ObservedAt = time.Now().Add(-time.Minute) },
		"rules digest":     func(value *jobRulesResponse) { value.RulesDigest = "bad" },
		"enforcement id":   func(value *jobRulesResponse) { value.EnforcementID = "bad id" },
		"network":          func(value *jobRulesResponse) { value.NetworkID = strings.Repeat("d", 64) },
		"missing lease":    func(value *jobRulesResponse) { value.LeaseID = "" },
		"zero observation": func(value *jobRulesResponse) { value.ObservedAt = time.Time{} },
	} {
		t.Run(name, func(t *testing.T) {
			verifier := startFakeJobRulesVerifier(t, func(req jobRulesRequest) any {
				reply := heldJobRules(req).(jobRulesResponse)
				mutate(&reply)
				return reply
			}, heldThroughRelease)
			if _, _, err := testClient(verifier.socketPath).AcquireJobRules(context.Background(), validJobRulesRequest()); err == nil {
				t.Fatal("job rules response that does not bind the job was accepted")
			}
		})
	}
}

func TestHeldJobRulesFailWhenTheVerifierDisconnects(t *testing.T) {
	verifier := startFakeJobRulesVerifier(t, heldJobRules, func(releaseRequest) any { return nil })
	lease, _, err := testClient(verifier.socketPath).AcquireJobRules(context.Background(), validJobRulesRequest())
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := lease.Complete(ctx); err == nil {
		t.Fatal("release without a held-through acknowledgement succeeded")
	}
	select {
	case err := <-lease.Failures():
		if err == nil {
			t.Fatal("verifier disconnect reported no failure")
		}
	case <-time.After(time.Second):
		t.Fatal("verifier disconnect was not reported")
	}
}

func TestHeldJobRulesRejectAVersionOneReleaseAcknowledgement(t *testing.T) {
	verifier := startFakeJobRulesVerifier(t, heldJobRules, func(rel releaseRequest) any {
		ack := heldThroughRelease(rel).(releaseResponse)
		ack.SchemaVersion = SchemaVersion
		return ack
	})
	lease, _, err := testClient(verifier.socketPath).AcquireJobRules(context.Background(), validJobRulesRequest())
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := lease.Complete(ctx); err == nil {
		t.Fatal("release acknowledgement with the wrong schema version accepted")
	}
}
