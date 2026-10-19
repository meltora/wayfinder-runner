package v1_test

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
)

type providerPullRequestCommandFixture struct {
	Version  int               `json:"version"`
	Type     v1.MessageType    `json:"type"`
	ID       string            `json:"id"`
	SentAt   string            `json:"sent_at"`
	RunnerID string            `json:"runner_id"`
	Payload  v1.CommandPayload `json:"payload"`
}

func TestProviderPullRequestCommandDigestMatchesControlPlaneFixtures(t *testing.T) {
	for path, digest := range map[string]string{
		"testdata/provider_pull_request_v1_observation_command.json": "202f6189e0e010269ba3169ed893d9440d9c94ab22932668966f36bc211203e0",
		"testdata/provider_pull_request_v1_merge_command.json":       "234a223ad652999ac81189bebd0390510189c0b5fbfc416777599afeb1f4156f",
	} {
		var fixture providerPullRequestCommandFixture
		decodeStrictFixture(t, path, &fixture)
		authority := fixture.Payload.ProviderPullRequest
		if authority == nil || authority.CommandDigest != digest {
			t.Fatalf("%s authority = %#v", path, authority)
		}
		if got := v1.ProviderPullRequestCommandDigest(*authority); got != digest {
			t.Fatalf("%s digest = %s", path, got)
		}
		if fixture.ID != v1.ProviderPullRequestCommandMessageID(authority.OperationID) {
			t.Fatalf("%s message id = %s", path, fixture.ID)
		}
	}
}

func TestProviderPullRequestResultEventRoundTripsControlPlaneFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/provider_pull_request_v1_result.json")
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Version  int                               `json:"version"`
		Type     v1.MessageType                    `json:"type"`
		ID       string                            `json:"id"`
		SentAt   string                            `json:"sent_at"`
		RunnerID string                            `json:"runner_id"`
		Payload  v1.ProviderPullRequestResultEvent `json:"payload"`
	}
	decodeStrictFixture(t, "testdata/provider_pull_request_v1_result.json", &envelope)
	event := envelope.Payload
	if envelope.ID != v1.ProviderPullRequestResultEventID(event.OperationID) {
		t.Fatalf("event id = %s", envelope.ID)
	}
	if event.Outcome.Kind != v1.ProviderPullRequestOutcomeMerged || event.Outcome.MergeCommitSHA == nil {
		t.Fatalf("outcome = %#v", event.Outcome)
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	var expected, actual map[string]any
	var fixture struct {
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(fixture.Payload, &expected)
	_ = json.Unmarshal(encoded, &actual)
	if !reflect.DeepEqual(expected, actual) {
		t.Fatalf("encoded = %s", encoded)
	}
}

func TestProviderPullRequestObservedOutcomeKeepsNullableFields(t *testing.T) {
	encoded, err := json.Marshal(v1.ProviderPullRequestResultOutcome{Kind: v1.ProviderPullRequestOutcomeObserved, State: "open", HeadSHA: "e31aeb2649bce0a55feed5433206cfb68f4fa94c"})
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"kind":"observed","state":"open","head_sha":"e31aeb2649bce0a55feed5433206cfb68f4fa94c","merge_commit_sha":null,"mergeable":null,"allowed_merge_methods":null}` {
		t.Fatalf("encoded = %s", encoded)
	}
	failed, _ := json.Marshal(v1.ProviderPullRequestResultOutcome{Kind: v1.ProviderPullRequestOutcomeFailed, Code: v1.ProviderPullRequestHeadChanged})
	if string(failed) != `{"kind":"failed","code":"head_changed"}` {
		t.Fatalf("failed = %s", failed)
	}
}
