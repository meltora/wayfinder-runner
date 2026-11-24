package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/previewhost"
	"github.com/example/app-runner/internal/validation"
)

const sharedPreviewDeployVectorsPath = "../../api/runner/v1/testdata/preview_deploy_v1_vectors.json"

var sharedPreviewDeployRuleErrors = map[string]string{
	"deploy_bad_command_id":                          "preview deploy command_message_id is invalid",
	"deploy_expires_at_sub_millisecond":              "preview deploy expires_at is invalid",
	"deploy_expires_at_equals_lease":                 "preview must remain valid beyond the deploy lease",
	"deploy_expires_at_before_lease":                 "preview must remain valid beyond the deploy lease",
	"deploy_blueprint_content_over_256_kib":          "preview deploy blueprint content size is invalid",
	"deploy_blueprint_digest_mismatch":               "preview deploy blueprint digest does not bind content",
	"deploy_blueprint_version_leading_zero":          "hosted preview blueprint version must be a positive decimal integer",
	"deploy_blueprint_version_not_decimal":           "hosted preview blueprint version must be a positive decimal integer",
	"deploy_blueprint_version_zero":                  "hosted preview blueprint version must be a positive decimal integer",
	"deploy_blueprint_version_17_digits":             "hosted preview blueprint version must be a positive decimal integer",
	"deploy_commit_set_unsorted":                     "preview observation commits must be unique and sorted",
	"deploy_commit_set_duplicate_repository":         "preview observation commits must be unique and sorted",
	"deploy_commit_set_over_16_repositories":         "preview observation commit_set is invalid",
	"deploy_commit_set_empty":                        "preview observation commit_set is invalid",
	"deploy_commit_sha_uppercase":                    "commit_sha must be a full lowercase Git commit SHA",
	"deploy_preview_key_invalid":                     "preview_key is invalid",
	"deploy_idempotency_key_invalid":                 "preview deploy idempotency_key is invalid",
	"deploy_operation_id_invalid":                    "preview_deploy_operation_id is invalid",
	"deploy_edge_host_foreign_key":                   "preview deploy edge_host is invalid",
	"deploy_operation_digest_not_resealed":           "preview deploy operation_digest does not bind stable intent",
	"deploy_resource_binding_digest_mismatch":        "preview deploy resource_binding_digest does not bind resource authority",
	"deploy_command_digest_mismatch":                 "preview deploy command_digest does not bind delivery authority",
	"deploy_commit_set_digest_mismatch":              "preview deploy commit_set_digest does not bind commit_set",
	"hosted_extend_without_desired_expiry":           "preview lifecycle desired_expires_at is invalid",
	"hosted_destroy_with_desired_expiry":             "destroy preview lifecycle operation forbids desired_expires_at",
	"hosted_lifecycle_bad_command_id":                "preview lifecycle command_message_id is invalid",
	"hosted_lifecycle_blueprint_version_not_decimal": "hosted preview blueprint version must be a positive decimal integer",
}

type sharedPreviewDeployVectors struct {
	Valid []struct {
		Name     string          `json:"name"`
		Given    string          `json:"given"`
		Envelope json.RawMessage `json:"envelope"`
		Expected struct {
			ResultEventID string `json:"result_event_id"`
		} `json:"expected"`
	} `json:"valid"`
	Invalid       []sharedPreviewDeployRejection `json:"invalid"`
	Stateful      []sharedPreviewDeployRejection `json:"stateful"`
	HostedResults []sharedHostedLifecycleResult  `json:"hosted_lifecycle_results"`
}

type sharedHostedLifecycleResult struct {
	Name            string          `json:"name"`
	Given           string          `json:"given"`
	Controller      string          `json:"controller"`
	CommandEnvelope json.RawMessage `json:"command_envelope"`
	ResultEnvelope  json.RawMessage `json:"result_envelope"`
	Expected        struct {
		ResultEventID string `json:"result_event_id"`
	} `json:"expected"`
}

type leaseOutlivingPreviewHostController struct {
	*fakePreviewHostController
}

func (controller leaseOutlivingPreviewHostController) ApplyLifecycle(ctx context.Context, request previewhost.LifecycleRequest) previewhost.LifecycleResult {
	<-ctx.Done()
	deadline, _ := ctx.Deadline()
	time.Sleep(time.Until(deadline.Add(2 * time.Millisecond)))
	return controller.fakePreviewHostController.ApplyLifecycle(ctx, request)
}

type sharedPreviewDeployRejection struct {
	Name            string          `json:"name"`
	Given           string          `json:"given"`
	Envelope        json.RawMessage `json:"envelope"`
	ExpectedOutcome string          `json:"expected_outcome"`
}

func strictJSON(t *testing.T, data []byte, out any) error {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		t.Fatal("trailing JSON")
	}
	return nil
}

func decodeSharedVectorEnvelope(t *testing.T, raw json.RawMessage) (v1.Envelope, v1.CommandPayload) {
	t.Helper()
	var envelope v1.Envelope
	if err := strictJSON(t, raw, &envelope); err != nil {
		t.Fatalf("envelope: %v", err)
	}
	payload, err := json.Marshal(envelope.Payload)
	if err != nil {
		t.Fatal(err)
	}
	var command v1.CommandPayload
	if err := strictJSON(t, payload, &command); err != nil {
		t.Fatalf("payload: %v", err)
	}
	return envelope, command
}

func loadSharedPreviewDeployVectors(t *testing.T) (sharedPreviewDeployVectors, map[string]v1.PreviewDeployOperation) {
	t.Helper()
	raw, err := os.ReadFile(sharedPreviewDeployVectorsPath)
	if err != nil {
		t.Fatal(err)
	}
	var vectors sharedPreviewDeployVectors
	if err := json.Unmarshal(raw, &vectors); err != nil {
		t.Fatal(err)
	}
	deploys := map[string]v1.PreviewDeployOperation{}
	for _, vector := range vectors.Valid {
		if _, command := decodeSharedVectorEnvelope(t, vector.Envelope); command.PreviewDeploy != nil {
			deploys[vector.Name] = *command.PreviewDeploy
		}
	}
	return vectors, deploys
}

func givenVectorDeploy(t *testing.T, deploys map[string]v1.PreviewDeployOperation, given, prefix string) v1.PreviewDeployOperation {
	t.Helper()
	deploy, ok := deploys[strings.TrimPrefix(given, prefix)]
	if !strings.HasPrefix(given, prefix) || !ok {
		t.Fatalf("given=%q", given)
	}
	return deploy
}

func TestSharedPreviewDeployVectorsAreAcceptedWithTheirEventIDs(t *testing.T) {
	vectors, deploys := loadSharedPreviewDeployVectors(t)
	for _, vector := range vectors.Valid {
		t.Run(vector.Name, func(t *testing.T) {
			jr, _ := testJobRunner(t)
			envelope, command := decodeSharedVectorEnvelope(t, vector.Envelope)
			controller := &fakePreviewHostController{deployOutcome: deployedPreviewOutcome()}
			jr.d.previewHostController = controller
			var published func() int
			switch command.Command {
			case v1.CmdPreviewDeploy:
				if vector.Given != "trusted_commit_set" {
					t.Fatalf("given=%q", vector.Given)
				}
				trustPreviewDeployCommits(t, jr.d, *command.PreviewDeploy)
				events := capturePreviewDeployEvents(t, jr.d)
				published = func() int { return len(*events) }
			case v1.CmdPreviewLifecycle:
				deploy := givenVectorDeploy(t, deploys, vector.Given, "deployed:")
				insertHostedPreviewHost(t, jr.d, deploy, nil)
				if command.PreviewLifecycle.Action == v1.PreviewLifecycleExtend {
					controller.lifecycleOutcome = hostedExtendSucceeded(*command.PreviewLifecycle)
				} else {
					controller.lifecycleOutcome = v1.PreviewLifecycleResultOutcome{
						Kind:          v1.PreviewLifecycleSucceeded,
						ResourceState: &v1.PreviewLifecycleResourceState{Kind: v1.PreviewLifecycleResourceDestroyed, Disposition: v1.PreviewLifecycleDestroyed},
					}
				}
				events := captureLifecycleEvents(jr.d)
				published = func() int { return len(*events) }
				if got := previewLifecycleResultEventID(envelope.ID); got != vector.Expected.ResultEventID {
					t.Fatalf("daemon lifecycle event id=%s want %s", got, vector.Expected.ResultEventID)
				}
			default:
				t.Fatalf("command=%q", command.Command)
			}
			if err := jr.d.executeCommand(context.Background(), envelope, command); err != nil {
				t.Fatal(err)
			}
			if published() != 1 {
				t.Fatalf("published=%d", published())
			}
			if exists, _ := jr.d.journal.HasRecord(context.Background(), vector.Expected.ResultEventID); !exists {
				t.Fatalf("result is not journalled under %s", vector.Expected.ResultEventID)
			}
		})
	}
}

func TestSharedPreviewDeployInvalidVectorsAreRejectedAsInvalidCommands(t *testing.T) {
	vectors, _ := loadSharedPreviewDeployVectors(t)
	if len(sharedPreviewDeployRuleErrors) != len(vectors.Invalid) {
		t.Fatalf("rule table has %d entries for %d invalid vectors", len(sharedPreviewDeployRuleErrors), len(vectors.Invalid))
	}
	for _, vector := range vectors.Invalid {
		t.Run(vector.Name, func(t *testing.T) {
			if vector.ExpectedOutcome != string(v1.CommandAckReasonInvalidCommand) || vector.Given != "none" {
				t.Fatalf("expected_outcome=%q given=%q", vector.ExpectedOutcome, vector.Given)
			}
			jr, _ := testJobRunner(t)
			controller := &fakePreviewHostController{deployOutcome: deployedPreviewOutcome()}
			jr.d.previewHostController = controller
			envelope, command := decodeSharedVectorEnvelope(t, vector.Envelope)
			err := jr.d.executeCommand(context.Background(), envelope, command)
			if reason := commandRejectionReason(err); reason != v1.CommandAckReasonInvalidCommand {
				t.Fatalf("reason=%q err=%v", reason, err)
			}
			want, ok := sharedPreviewDeployRuleErrors[vector.Name]
			if cause := errors.Unwrap(err); !ok || cause == nil || !strings.Contains(cause.Error(), want) {
				t.Fatalf("cause=%v, want the %q rule", cause, want)
			}
			if vector.Name == "deploy_blueprint_content_over_256_kib" {
				blueprint := command.PreviewDeploy.Blueprint
				if digest, err := v1.PreviewBlueprintDigestV1(blueprint.Content); err != nil || digest != blueprint.Digest {
					t.Fatalf("oversize content digest=%s err=%v", digest, err)
				}
				if _, err := validation.ParsePreviewBlueprintContentV1(blueprint.Content); err != nil {
					t.Fatalf("oversize content is not otherwise schema-valid: %v", err)
				}
			}
			if deploys, lifecycles, _ := controller.counts(); deploys != 0 || lifecycles != 0 {
				t.Fatalf("deploys=%d lifecycles=%d", deploys, lifecycles)
			}
		})
	}
}

func TestSharedPreviewDeployStatefulVectorsReachTheirOutcome(t *testing.T) {
	vectors, deploys := loadSharedPreviewDeployVectors(t)
	if len(vectors.Stateful) == 0 {
		t.Fatal("no stateful vectors")
	}
	for _, vector := range vectors.Stateful {
		t.Run(vector.Name, func(t *testing.T) {
			jr, _ := testJobRunner(t)
			controller := &fakePreviewHostController{deployOutcome: deployedPreviewOutcome()}
			jr.d.previewHostController = controller
			events := capturePreviewDeployEvents(t, jr.d)
			envelope, command := decodeSharedVectorEnvelope(t, vector.Envelope)
			switch {
			case strings.HasPrefix(vector.Given, "trusted_commit_set:"):
				trustPreviewDeployCommits(t, jr.d, givenVectorDeploy(t, deploys, vector.Given, "trusted_commit_set:"))
			case strings.HasPrefix(vector.Given, "deployed:"):
				deploy := givenVectorDeploy(t, deploys, vector.Given, "deployed:")
				trustPreviewDeployCommits(t, jr.d, deploy)
				first := v1.CommandPayload{Command: v1.CmdPreviewDeploy, PreviewDeploy: &deploy}
				if err := jr.d.executeCommand(context.Background(), v1.Envelope{ID: previewDeployCommandID("f")}, first); err != nil {
					t.Fatal(err)
				}
				*events = nil
			default:
				t.Fatalf("given=%q", vector.Given)
			}
			before, _, _ := controller.counts()
			err := jr.d.executeCommand(context.Background(), envelope, command)
			after, _, _ := controller.counts()
			switch vector.ExpectedOutcome {
			case "failed:commit_set":
				if err != nil || len(*events) != 1 || (*events)[0].Outcome.Kind != v1.PreviewDeployFailed ||
					(*events)[0].Outcome.Phase != v1.PreviewDeployPhaseCommitSet || after != before {
					t.Fatalf("err=%v events=%+v deploys=%d->%d", err, *events, before, after)
				}
			case string(v1.CommandAckReasonCommandConflict):
				if reason := commandRejectionReason(err); reason != v1.CommandAckReasonCommandConflict || len(*events) != 0 || after != before {
					t.Fatalf("reason=%q err=%v events=%d deploys=%d->%d", reason, err, len(*events), before, after)
				}
			default:
				t.Fatalf("expected_outcome=%q", vector.ExpectedOutcome)
			}
		})
	}
}

func decodeSharedResultEnvelope(t *testing.T, raw []byte) (map[string]any, v1.PreviewLifecycleResultEvent) {
	t.Helper()
	var envelope map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	var event v1.PreviewLifecycleResultEvent
	if err := strictJSON(t, wire.Payload, &event); err != nil {
		t.Fatalf("result payload: %v", err)
	}
	return envelope, event
}

func TestSharedHostedLifecycleResultVectorsAreValidHostedResults(t *testing.T) {
	vectors, _ := loadSharedPreviewDeployVectors(t)
	if len(vectors.HostedResults) != 4 {
		t.Fatalf("hosted lifecycle result vectors = %d", len(vectors.HostedResults))
	}
	for _, vector := range vectors.HostedResults {
		t.Run(vector.Name, func(t *testing.T) {
			envelope, event := decodeSharedResultEnvelope(t, vector.ResultEnvelope)
			if err := validation.PreviewHostedLifecycleResultEvent(event); err != nil {
				t.Fatal(err)
			}
			if envelope["id"] != vector.Expected.ResultEventID || envelope["id"] != previewLifecycleResultEventID(event.CommandMessageID) {
				t.Fatalf("result envelope id=%v want %s", envelope["id"], vector.Expected.ResultEventID)
			}
		})
	}
}

func TestSharedHostedLifecycleResultVectorsAreWhatTheDaemonPublishes(t *testing.T) {
	vectors, deploys := loadSharedPreviewDeployVectors(t)
	for _, vector := range vectors.HostedResults {
		t.Run(vector.Name, func(t *testing.T) {
			ctx := context.Background()
			jr, _ := testJobRunner(t)
			jr.d.runnerID = "runner-1"
			envelope, command := decodeSharedVectorEnvelope(t, vector.CommandEnvelope)
			operation := *command.PreviewLifecycle
			controller := &fakePreviewHostController{}
			jr.d.previewHostController = controller
			wantCalls := 1
			switch vector.Controller {
			case "extends":
				controller.lifecycleOutcome = hostedExtendSucceeded(operation)
			case "not_called":
				wantCalls = 0
			case "denies":
				controller.lifecycleOutcome = v1.PreviewLifecycleResultOutcome{Kind: v1.PreviewLifecyclePermanent, Code: v1.PreviewLifecyclePolicyDenied}
			case "outlives_lease":
				controller.lifecycleOutcome = v1.PreviewLifecycleResultOutcome{
					Kind:          v1.PreviewLifecycleSucceeded,
					ResourceState: &v1.PreviewLifecycleResourceState{Kind: v1.PreviewLifecycleResourceDestroyed, Disposition: v1.PreviewLifecycleDestroyed},
				}
				jr.d.previewHostController = leaseOutlivingPreviewHostController{controller}
			default:
				t.Fatalf("controller=%q", vector.Controller)
			}
			if vector.Given != "none" {
				insertHostedPreviewHost(t, jr.d, givenVectorDeploy(t, deploys, vector.Given, "deployed:"), nil)
			}
			events := captureLifecycleEvents(jr.d)
			if vector.Controller == "outlives_lease" {
				operation.LeaseExpiresAt = time.Now().UTC().Truncate(time.Millisecond).Add(time.Second)
				sealHostedPreviewLifecycle(&operation)
				command.PreviewLifecycle = &operation
			}
			if err := jr.d.executeCommand(ctx, envelope, command); err != nil {
				t.Fatal(err)
			}
			if _, calls, _ := controller.counts(); calls != wantCalls || len(*events) != 1 {
				t.Fatalf("controller calls=%d want %d, published=%d", calls, wantCalls, len(*events))
			}
			record, found, err := jr.d.journal.LoadRecord(ctx, vector.Expected.ResultEventID)
			if err != nil || !found {
				t.Fatalf("result is not journalled under %s: found=%v err=%v", vector.Expected.ResultEventID, found, err)
			}
			got, event := decodeSharedResultEnvelope(t, record.Payload)
			want, _ := decodeSharedResultEnvelope(t, vector.ResultEnvelope)
			if event.ObservedAt.After(event.LeaseExpiresAt) {
				t.Fatalf("observed_at %s is after lease_expires_at %s", event.ObservedAt, event.LeaseExpiresAt)
			}
			gotPayload, wantPayload := got["payload"].(map[string]any), want["payload"].(map[string]any)
			if vector.Controller == "outlives_lease" {
				if !event.ObservedAt.Equal(event.LeaseExpiresAt) {
					t.Fatalf("timeout observed_at %s is not lease_expires_at %s", event.ObservedAt, event.LeaseExpiresAt)
				}
				gotPayload["lease_expires_at"] = wantPayload["lease_expires_at"]
				gotPayload["command_digest"] = wantPayload["command_digest"]
			}
			gotPayload["observed_at"] = wantPayload["observed_at"]
			got["sent_at"] = want["sent_at"]
			if !reflect.DeepEqual(got, want) {
				gotJSON, _ := json.Marshal(got)
				wantJSON, _ := json.Marshal(want)
				t.Fatalf("daemon published\n%s\nwant\n%s", gotJSON, wantJSON)
			}
		})
	}
}
