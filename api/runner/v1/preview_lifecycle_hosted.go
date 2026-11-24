package v1

import (
	"bytes"
	"encoding/json"
	"fmt"
)

var previewHostedLifecycleAbsentFields = map[string]struct{}{
	"preview_evidence_id":        {},
	"preview_observation_id":     {},
	"preview_observation_digest": {},
	"preview_url":                {},
}

func (operation PreviewLifecycleOperation) Hosted() bool {
	return operation.PreviewID != ""
}

func (authority PreviewLifecycleResultAuthority) Hosted() bool {
	return authority.PreviewID != ""
}

func (operation PreviewLifecycleOperation) MarshalJSON() ([]byte, error) {
	type wire PreviewLifecycleOperation
	raw, err := json.Marshal(wire(operation))
	if err != nil || !operation.Hosted() {
		return raw, err
	}
	return withoutTopLevelFields(raw, previewHostedLifecycleAbsentFields)
}

func (event PreviewLifecycleResultEvent) MarshalJSON() ([]byte, error) {
	type wire PreviewLifecycleResultEvent
	raw, err := json.Marshal(wire(event))
	if err != nil || !event.Hosted() {
		return raw, err
	}
	return withoutTopLevelFields(raw, previewHostedLifecycleAbsentFields)
}

func withoutTopLevelFields(raw []byte, drop map[string]struct{}) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return nil, fmt.Errorf("encode hosted preview lifecycle: object expected")
	}
	var out bytes.Buffer
	out.WriteByte('{')
	first := true
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := token.(string)
		if !ok {
			return nil, fmt.Errorf("encode hosted preview lifecycle: key expected")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		if _, skip := drop[key]; skip {
			continue
		}
		if !first {
			out.WriteByte(',')
		}
		first = false
		encodedKey, err := json.Marshal(key)
		if err != nil {
			return nil, err
		}
		out.Write(encodedKey)
		out.WriteByte(':')
		out.Write(value)
	}
	out.WriteByte('}')
	return out.Bytes(), nil
}

func previewHostedLifecycleBindingMaterial(operation PreviewLifecycleOperation) map[string]any {
	return map[string]any{
		"organization_id":           operation.OrganizationID,
		"project_id":                operation.ProjectID,
		"workflow_run_id":           operation.WorkflowRunID,
		"preview_id":                operation.PreviewID,
		"preview_key":               operation.PreviewKey,
		"execution_manifest_id":     operation.ExecutionManifestID,
		"execution_manifest_digest": operation.ExecutionManifestDigest,
		"blueprint":                 previewCanonicalBlueprint(operation.Blueprint),
		"commit_set":                previewCanonicalCommits(operation.CommitSet),
		"commit_set_digest":         operation.CommitSetDigest,
		"original_expires_at":       previewLifecycleCanonicalTime(operation.OriginalExpiresAt),
	}
}

func PreviewHostedLifecycleResourceBindingDigest(operation PreviewLifecycleOperation) string {
	return PreviewCanonicalDigest(previewHostedLifecycleBindingMaterial(operation))
}

func PreviewHostedLifecycleOperationDigest(operation PreviewLifecycleOperation) string {
	material := previewHostedLifecycleBindingMaterial(operation)
	material["schema_version"] = previewCanonicalInteger(uint64(operation.SchemaVersion))
	material["preview_lifecycle_effect_id"] = operation.PreviewLifecycleEffectID
	material["preview_lifecycle_effect_revision"] = previewCanonicalInteger(operation.PreviewLifecycleEffectRevision)
	material["preview_lifecycle_operation_id"] = operation.PreviewLifecycleOperationID
	material["current_effective_expires_at"] = previewLifecycleCanonicalTime(operation.CurrentEffectiveExpiresAt)
	material["action"] = string(operation.Action)
	if operation.DesiredExpiresAt != nil {
		material["desired_expires_at"] = previewLifecycleCanonicalTime(*operation.DesiredExpiresAt)
	}
	material["idempotency_key"] = operation.IdempotencyKey
	material["resource_binding_digest"] = operation.ResourceBindingDigest
	return PreviewCanonicalDigest(material)
}

func PreviewHostedLifecycleReceiptDigest(operation PreviewLifecycleOperation, state PreviewLifecycleResourceState) string {
	material := map[string]any{
		"operation_digest":        operation.OperationDigest,
		"resource_binding_digest": operation.ResourceBindingDigest,
		"resource_kind":           string(state.Kind),
		"disposition":             string(state.Disposition),
	}
	if state.ExpiresAt != nil {
		material["expires_at"] = previewLifecycleCanonicalTime(*state.ExpiresAt)
	}
	return PreviewCanonicalDigest(material)
}
