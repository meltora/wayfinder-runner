package previewlifecycle

import (
	"context"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/config"
)

func lifecycleOperation(action v1.PreviewLifecycleAction) v1.PreviewLifecycleOperation {
	now := time.Now().UTC()
	operation := v1.PreviewLifecycleOperation{
		PreviewLifecycleOperationAuthority: v1.PreviewLifecycleOperationAuthority{
			SchemaVersion:            v1.PreviewLifecycleSchemaVersion,
			PreviewLifecycleEffectID: "preview-lifecycle-effect-1", PreviewLifecycleEffectRevision: 1,
			PreviewLifecycleOperationID: "preview-lifecycle-operation-" + strings.Repeat("a", 64),
			OrganizationID:              "organization-1", ProjectID: "project-1", WorkflowRunID: "workflow-1",
			PreviewEvidenceID: "preview-evidence-1", PreviewObservationID: "preview-observation-1",
			PreviewObservationDigest: strings.Repeat("b", 64),
			ExecutionManifestID:      "manifest-1", ExecutionManifestDigest: strings.Repeat("c", 64),
			Action: action, IdempotencyKey: "preview-lifecycle-" + strings.Repeat("d", 64),
			ResourceBindingDigest: strings.Repeat("e", 64), OperationDigest: strings.Repeat("f", 64), CommandDigest: strings.Repeat("1", 64),
			LeaseID: "lease-1", FencingToken: 9, LeaseExpiresAt: now.Add(time.Minute),
		},
		Blueprint:       v1.PreviewObservationBlueprint{BlueprintID: "node-web", Version: "1", Digest: strings.Repeat("2", 64)},
		PreviewURL:      "https://branch.preview.example.test/",
		CommitSet:       []v1.PreviewObservationCommit{{RepositoryID: "web", CommitSHA: strings.Repeat("3", 40)}},
		CommitSetDigest: strings.Repeat("4", 64), OriginalExpiresAt: now.Add(time.Hour), CurrentEffectiveExpiresAt: now.Add(time.Hour),
	}
	if action == v1.PreviewLifecycleExtend {
		desired := now.Add(2 * time.Hour)
		operation.DesiredExpiresAt = &desired
	}
	operation.CommitSetDigest = v1.PreviewLifecycleCommitSetDigest(operation.CommitSet)
	operation.ResourceBindingDigest = v1.PreviewLifecycleResourceBindingDigest(operation)
	operation.OperationDigest = v1.PreviewLifecycleOperationDigest(operation)
	operation.CommandDigest = v1.PreviewLifecycleCommandDigest(operation)
	return operation
}

func controllerConfig(controllerURL string) config.PreviewLifecycleControllerConfig {
	return config.PreviewLifecycleControllerConfig{
		BlueprintID: "node-web", Version: "1", Digest: strings.Repeat("2", 64), ControllerURL: controllerURL,
		Actions: []string{"destroy", "extend"}, TimeoutSeconds: 2, MaxResponseBytes: 64 << 10,
		MaxExtensionMS: uint64(30 * 24 * time.Hour / time.Millisecond),
	}
}

func writeTLSFiles(t *testing.T, server *httptest.Server) (string, string) {
	t.Helper()
	dir := t.TempDir()
	caPath := filepath.Join(dir, "ca.pem")
	cert := server.Certificate()
	if cert == nil {
		t.Fatal("test TLS server certificate missing")
	}
	if err := os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	authPath := filepath.Join(dir, "authorization")
	if err := os.WriteFile(authPath, []byte("Bearer runner-local-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return caPath, authPath
}

func successResponse(operation v1.PreviewLifecycleOperation) string {
	if operation.Action == v1.PreviewLifecycleDestroy {
		return `{"controller_protocol_version":1,"controller_receipt_digest":"` + strings.Repeat("6", 64) + `","resource_binding_digest":"` + operation.ResourceBindingDigest + `","resource_state":{"kind":"destroyed","disposition":"destroyed"}}`
	}
	return `{"controller_protocol_version":1,"controller_receipt_digest":"` + strings.Repeat("6", 64) + `","resource_binding_digest":"` + operation.ResourceBindingDigest + `","resource_state":{"kind":"active","preview_url":"` + operation.PreviewURL + `","blueprint":{"blueprint_id":"` + operation.Blueprint.BlueprintID + `","version":"` + operation.Blueprint.Version + `","digest":"` + operation.Blueprint.Digest + `"},"commit_set":[{"repository_id":"web","commit_sha":"` + strings.Repeat("3", 40) + `"}],"expires_at":"` + operation.DesiredExpiresAt.Format(time.RFC3339Nano) + `"}}`
}

func TestRegistryAdvertisesOnlyReadyExactControllerCapabilities(t *testing.T) {
	controller := controllerConfig("https://controller.example.test/v1/preview-lifecycle")
	registry, err := NewRegistry(map[string]config.PreviewLifecycleControllerConfig{"node": controller}, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	profiles := registry.ReadyProfiles()
	if len(profiles) != 1 || profiles[0].BlueprintID != controller.BlueprintID || profiles[0].ControllerProtocolVersion != 1 ||
		strings.Join([]string{string(profiles[0].Actions[0]), string(profiles[0].Actions[1])}, ",") != "destroy,extend" ||
		profiles[0].MaxExtensionMS == nil || *profiles[0].MaxExtensionMS != controller.MaxExtensionMS {
		t.Fatalf("capabilities = %#v", profiles)
	}
	profiles[0].Actions[0] = "changed"
	*profiles[0].MaxExtensionMS = 1
	if current := registry.ReadyProfiles()[0]; current.Actions[0] != v1.PreviewLifecycleDestroy || *current.MaxExtensionMS != controller.MaxExtensionMS {
		t.Fatal("caller mutated registry capability")
	}
}

func TestRegistryAppliesExactDesiredStateOverFixedAuthenticatedHTTPSController(t *testing.T) {
	operation := lifecycleOperation(v1.PreviewLifecycleExtend)
	var requestSeen bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requestSeen = true
		if request.URL.Path != "/v1/preview-lifecycle" || request.Header.Get("Authorization") != "Bearer runner-local-secret" ||
			request.Header.Get("Idempotency-Key") != operation.IdempotencyKey || request.Header.Get("X-Preview-Lifecycle-Fence") != "9" {
			t.Errorf("request boundary path=%q headers=%v", request.URL.Path, request.Header)
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(successResponse(operation)))
	}))
	defer server.Close()
	caPath, authPath := writeTLSFiles(t, server)
	controller := controllerConfig(server.URL + "/v1/preview-lifecycle")
	controller.TrustedCAFile, controller.AuthorizationFile = caPath, authPath
	registry, err := NewRegistry(map[string]config.PreviewLifecycleControllerConfig{"node": controller}, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	outcome := registry.Apply(context.Background(), operation)
	if !requestSeen || outcome.Kind != v1.PreviewLifecycleSucceeded || outcome.ResourceState == nil ||
		outcome.ResourceState.ExpiresAt == nil || !outcome.ResourceState.ExpiresAt.Equal(*operation.DesiredExpiresAt) {
		t.Fatalf("outcome = %+v", outcome)
	}
}

func TestRegistryNeverFollowsRedirectsOrReturnsFalseSuccess(t *testing.T) {
	var redirected bool
	target := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected = true }))
	defer target.Close()
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		http.Redirect(response, request, target.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	caPath, authPath := writeTLSFiles(t, server)
	controller := controllerConfig(server.URL)
	controller.TrustedCAFile, controller.AuthorizationFile = caPath, authPath
	registry, err := NewRegistry(map[string]config.PreviewLifecycleControllerConfig{"node": controller}, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	outcome := registry.Apply(context.Background(), lifecycleOperation(v1.PreviewLifecycleDestroy))
	if redirected || outcome.Kind == v1.PreviewLifecycleSucceeded {
		t.Fatalf("redirected=%v outcome=%+v", redirected, outcome)
	}
}

func TestRegistryClassifiesResponseLossAndTimeoutAsUncertain(t *testing.T) {
	operation := lifecycleOperation(v1.PreviewLifecycleDestroy)
	registry, err := NewRegistry(map[string]config.PreviewLifecycleControllerConfig{
		"node": controllerConfig("https://127.0.0.1:1/v1/preview-lifecycle"),
	}, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	outcome := registry.Apply(context.Background(), operation)
	if outcome.Kind != v1.PreviewLifecycleUncertain || outcome.Code != v1.PreviewLifecycleReceiptUnavailable {
		t.Fatalf("response loss = %+v", outcome)
	}
	expiredCtx, expiredCancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer expiredCancel()
	outcome = registry.Apply(expiredCtx, operation)
	if outcome.Kind != v1.PreviewLifecycleUncertain || outcome.Code != v1.PreviewLifecycleControllerTimeout {
		t.Fatalf("expired caller context = %+v", outcome)
	}

	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { time.Sleep(200 * time.Millisecond) }))
	defer server.Close()
	caPath, _ := writeTLSFiles(t, server)
	controller := controllerConfig(server.URL)
	controller.TrustedCAFile = caPath
	controller.TimeoutSeconds = 1
	registry, err = NewRegistry(map[string]config.PreviewLifecycleControllerConfig{"node": controller}, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	outcome = registry.Apply(ctx, operation)
	if outcome.Kind != v1.PreviewLifecycleUncertain || outcome.Code != v1.PreviewLifecycleControllerTimeout {
		t.Fatalf("timeout = %+v", outcome)
	}
}

func TestRegistryRejectsForeignOrInvalidControllerResult(t *testing.T) {
	operation := lifecycleOperation(v1.PreviewLifecycleExtend)
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(strings.Replace(successResponse(operation), operation.ResourceBindingDigest, strings.Repeat("9", 64), 1)))
	}))
	defer server.Close()
	caPath, _ := writeTLSFiles(t, server)
	controller := controllerConfig(server.URL)
	controller.TrustedCAFile = caPath
	registry, err := NewRegistry(map[string]config.PreviewLifecycleControllerConfig{"node": controller}, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	outcome := registry.Apply(context.Background(), operation)
	if outcome.Kind != v1.PreviewLifecycleUncertain || outcome.Code != v1.PreviewLifecycleReceiptUnavailable {
		t.Fatalf("invalid response = %+v", outcome)
	}
}

func TestRegistryTreatsResponseLossAfterControllerReceivesEffectAsUncertain(t *testing.T) {
	operation := lifecycleOperation(v1.PreviewLifecycleDestroy)
	received := make(chan struct{}, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		_, _ = io.Copy(io.Discard, request.Body)
		received <- struct{}{}
		hijacker, ok := response.(http.Hijacker)
		if !ok {
			t.Error("response writer cannot simulate response loss")
			return
		}
		connection, _, err := hijacker.Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		_ = connection.Close()
	}))
	defer server.Close()
	caPath, _ := writeTLSFiles(t, server)
	controller := controllerConfig(server.URL)
	controller.TrustedCAFile = caPath
	registry, err := NewRegistry(map[string]config.PreviewLifecycleControllerConfig{"node": controller}, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	outcome := registry.Apply(context.Background(), operation)
	select {
	case <-received:
	default:
		t.Fatal("controller did not receive effect")
	}
	if outcome.Kind != v1.PreviewLifecycleUncertain || outcome.Code != v1.PreviewLifecycleReceiptUnavailable {
		t.Fatalf("response loss after effect = %+v", outcome)
	}
}

func TestRegistryMapsOnlyDefinitePreMutationHTTPResponses(t *testing.T) {
	status := http.StatusForbidden
	retryAfterValue := "3"
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		if status == http.StatusTooManyRequests {
			response.Header().Set("Retry-After", retryAfterValue)
		}
		response.WriteHeader(status)
	}))
	defer server.Close()
	caPath, _ := writeTLSFiles(t, server)
	controller := controllerConfig(server.URL)
	controller.TrustedCAFile = caPath
	registry, err := NewRegistry(map[string]config.PreviewLifecycleControllerConfig{"node": controller}, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		status int
		kind   v1.PreviewLifecycleOutcomeKind
		code   v1.PreviewLifecycleErrorCode
	}{
		{http.StatusUnauthorized, v1.PreviewLifecyclePermanent, v1.PreviewLifecyclePolicyDenied},
		{http.StatusForbidden, v1.PreviewLifecyclePermanent, v1.PreviewLifecyclePolicyDenied},
		{http.StatusNotFound, v1.PreviewLifecyclePermanent, v1.PreviewLifecycleResourceNotFound},
		{http.StatusConflict, v1.PreviewLifecycleRetryable, v1.PreviewLifecycleTransientResourceConflict},
		{http.StatusTooManyRequests, v1.PreviewLifecycleRetryable, v1.PreviewLifecycleRateLimited},
		{http.StatusBadGateway, v1.PreviewLifecycleUncertain, v1.PreviewLifecycleReceiptUnavailable},
		{http.StatusTeapot, v1.PreviewLifecyclePermanent, v1.PreviewLifecycleInvalidControllerResponse},
	}
	for _, test := range tests {
		status = test.status
		outcome := registry.Apply(context.Background(), lifecycleOperation(v1.PreviewLifecycleDestroy))
		if outcome.Kind != test.kind || outcome.Code != test.code {
			t.Fatalf("status %d outcome=%+v", test.status, outcome)
		}
		if test.status == http.StatusTooManyRequests && (outcome.RetryAfterMS == nil || *outcome.RetryAfterMS != 3000) {
			t.Fatalf("rate limit retry=%+v", outcome.RetryAfterMS)
		}
	}
	status = http.StatusTooManyRequests
	retryAfterValue = "0"
	zeroRetry := registry.Apply(context.Background(), lifecycleOperation(v1.PreviewLifecycleDestroy))
	if zeroRetry.Kind != v1.PreviewLifecycleRetryable || zeroRetry.RetryAfterMS == nil || *zeroRetry.RetryAfterMS != 0 {
		t.Fatalf("zero-second retry response=%+v", zeroRetry)
	}
}

func TestRegistryWithdrawsCapabilityWhenCredentialBecomesUnsafe(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	caPath, authPath := writeTLSFiles(t, server)
	controller := controllerConfig(server.URL)
	controller.TrustedCAFile, controller.AuthorizationFile = caPath, authPath
	registry, err := NewRegistry(map[string]config.PreviewLifecycleControllerConfig{"node": controller}, Dependencies{})
	if err != nil || len(registry.ReadyProfiles()) != 1 {
		t.Fatalf("ready=%v err=%v", registry.ReadyProfiles(), err)
	}
	if err := os.Chmod(authPath, 0o640); err != nil {
		t.Fatal(err)
	}
	if profiles := registry.ReadyProfiles(); len(profiles) != 0 {
		t.Fatalf("unsafe credential remained advertised: %#v", profiles)
	}
	if outcome := registry.Apply(context.Background(), lifecycleOperation(v1.PreviewLifecycleDestroy)); outcome.Kind != v1.PreviewLifecyclePermanent || outcome.Code != v1.PreviewLifecycleProfileUnavailable {
		t.Fatalf("unsafe credential remained executable: %+v", outcome)
	}
}
