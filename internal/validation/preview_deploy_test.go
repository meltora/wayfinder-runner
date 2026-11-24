package validation

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
)

const validPreviewDeployBlueprintContent = `{"schema_version":1,"services":[{"name":"db","role":"database","image":"docker.io/library/postgres@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","env":{"POSTGRES_PASSWORD":{"kind":"generated","generator":"random_secret"}}}],"app":{"image":"ghcr.io/acme/node@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","working_directory":".","install":["pnpm install --frozen-lockfile"],"build":["pnpm build"],"migrate":["pnpm db:migrate:deploy"],"seed":["pnpm db:seed"],"start":"pnpm start","port":3000,"health_checks":[{"id":"home","path":"/","expected_status":200}],"env":{"DATABASE_URL":{"kind":"generated","generator":"postgres_url"},"STRIPE_SECRET_KEY":{"kind":"secret","note":"Test mode key"},"FEATURE_FLAG":{"kind":"fake","value":"enabled"},"SENTRY_DSN":{"kind":"disabled"}}},"limits":{"memory_mb":1024,"cpus":1,"max_lifetime_minutes":1440},"egress":{"mode":"none"}}`

func sealPreviewDeployOperation(operation v1.PreviewDeployOperation) v1.PreviewDeployOperation {
	operation.CommitSetDigest = v1.PreviewLifecycleCommitSetDigest(operation.CommitSet)
	operation.ResourceBindingDigest = v1.PreviewDeployResourceBindingDigest(operation)
	operation.OperationDigest = v1.PreviewDeployOperationDigest(operation)
	operation.CommandDigest = v1.PreviewDeployCommandDigest(operation)
	return operation
}

func validPreviewDeployOperation() v1.PreviewDeployOperation {
	return sealPreviewDeployOperation(v1.PreviewDeployOperation{
		PreviewDeployOperationAuthority: v1.PreviewDeployOperationAuthority{
			SchemaVersion:               v1.PreviewDeploySchemaVersion,
			PreviewDeployEffectID:       "preview-deploy-effect-1",
			PreviewDeployEffectRevision: 1,
			PreviewDeployOperationID:    "preview-deploy-operation-" + strings.Repeat("a", 64),
			PreviewID:                   "preview-1",
			OrganizationID:              "organization-1", ProjectID: "project-1", WorkflowRunID: "workflow-1",
			ExecutionManifestID: "manifest-1", ExecutionManifestDigest: strings.Repeat("c", 64),
			IdempotencyKey: "preview-deploy-" + strings.Repeat("d", 64),
			LeaseID:        "lease-1", FencingToken: 4,
			LeaseExpiresAt: time.Date(2030, 8, 14, 12, 45, 0, 0, time.UTC),
		},
		Blueprint: v1.PreviewDeployBlueprint{
			BlueprintID: "node-web", Version: "1",
			Digest:  "83ded9d685a2c763c73dd2ea0f56a054fe1e851b8b44c8307211ea518fa60563",
			Content: json.RawMessage(validPreviewDeployBlueprintContent),
		},
		CommitSet: []v1.PreviewObservationCommit{
			{RepositoryID: "api", CommitSHA: strings.Repeat("1", 40)},
			{RepositoryID: "web", CommitSHA: strings.Repeat("2", 40)},
		},
		PreviewKey: "abcdefghijklmnopqrstuvwxyz",
		ExpiresAt:  time.Date(2030, 8, 14, 18, 0, 0, 0, time.UTC),
	})
}

func TestPreviewDeployOperationAcceptsSealedCommand(t *testing.T) {
	if err := PreviewDeployOperation(validPreviewDeployOperation()); err != nil {
		t.Fatal(err)
	}
	reformatted := validPreviewDeployOperation()
	reformatted.Blueprint.Content = json.RawMessage(" " + validPreviewDeployBlueprintContent + "\n")
	if err := PreviewDeployOperation(sealPreviewDeployOperation(reformatted)); err != nil {
		t.Fatalf("reformatted content rejected: %v", err)
	}
	withEdge := validPreviewDeployOperation()
	withEdge.EdgeHost = withEdge.PreviewKey + ".previews.example.test"
	if err := PreviewDeployOperation(sealPreviewDeployOperation(withEdge)); err != nil {
		t.Fatalf("edge host rejected: %v", err)
	}
}

func TestPreviewDeployOperationRejectsEveryBoundary(t *testing.T) {
	invalidContent := strings.Replace(validPreviewDeployBlueprintContent, `"memory_mb":1024`, `"memory_mb":0`, 1)
	invalidDigest, err := v1.PreviewBlueprintDigestV1([]byte(invalidContent))
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]struct {
		mutate func(*v1.PreviewDeployOperation)
		reseal bool
	}{
		"schema":          {func(o *v1.PreviewDeployOperation) { o.SchemaVersion = 2 }, true},
		"effect id":       {func(o *v1.PreviewDeployOperation) { o.PreviewDeployEffectID = "" }, true},
		"revision zero":   {func(o *v1.PreviewDeployOperation) { o.PreviewDeployEffectRevision = 0 }, true},
		"revision unsafe": {func(o *v1.PreviewDeployOperation) { o.PreviewDeployEffectRevision = 1 << 53 }, true},
		"operation id": {func(o *v1.PreviewDeployOperation) {
			o.PreviewDeployOperationID = "preview-deploy-operation-" + strings.Repeat("A", 64)
		}, true},
		"preview id":             {func(o *v1.PreviewDeployOperation) { o.PreviewID = "" }, true},
		"organization":           {func(o *v1.PreviewDeployOperation) { o.OrganizationID = "" }, true},
		"workflow":               {func(o *v1.PreviewDeployOperation) { o.WorkflowRunID = "" }, true},
		"manifest digest":        {func(o *v1.PreviewDeployOperation) { o.ExecutionManifestDigest = "c" }, true},
		"idempotency":            {func(o *v1.PreviewDeployOperation) { o.IdempotencyKey = "preview-lifecycle-" + strings.Repeat("d", 64) }, true},
		"fencing token":          {func(o *v1.PreviewDeployOperation) { o.FencingToken = 0 }, true},
		"lease":                  {func(o *v1.PreviewDeployOperation) { o.LeaseExpiresAt = time.Time{} }, true},
		"lease id":               {func(o *v1.PreviewDeployOperation) { o.LeaseID = "" }, true},
		"preview key upper":      {func(o *v1.PreviewDeployOperation) { o.PreviewKey = "ABCDEFGHIJKLMNOPQRSTUVWXYZ" }, true},
		"preview key short":      {func(o *v1.PreviewDeployOperation) { o.PreviewKey = "abcdefghijklmnopqrstuvwxy" }, true},
		"preview key digit":      {func(o *v1.PreviewDeployOperation) { o.PreviewKey = "abcdefghijklmnopqrstuvwxy1" }, true},
		"edge host other key":    {func(o *v1.PreviewDeployOperation) { o.EdgeHost = "zzzzzzzzzzzzzzzzzzzzzzzzzz.previews.example.test" }, true},
		"edge host bare key":     {func(o *v1.PreviewDeployOperation) { o.EdgeHost = o.PreviewKey }, true},
		"edge host empty domain": {func(o *v1.PreviewDeployOperation) { o.EdgeHost = o.PreviewKey + "." }, true},
		"edge host bad domain":   {func(o *v1.PreviewDeployOperation) { o.EdgeHost = o.PreviewKey + ".exa mple.test" }, true},
		"expires zero":           {func(o *v1.PreviewDeployOperation) { o.ExpiresAt = time.Time{} }, true},
		"expires before lease":   {func(o *v1.PreviewDeployOperation) { o.ExpiresAt = o.LeaseExpiresAt }, true},
		"expires sub-ms":         {func(o *v1.PreviewDeployOperation) { o.ExpiresAt = o.ExpiresAt.Add(time.Microsecond) }, true},
		"no commits":             {func(o *v1.PreviewDeployOperation) { o.CommitSet = nil }, true},
		"unsorted commits": {func(o *v1.PreviewDeployOperation) {
			o.CommitSet[0], o.CommitSet[1] = o.CommitSet[1], o.CommitSet[0]
		}, true},
		"duplicate commits": {func(o *v1.PreviewDeployOperation) {
			o.CommitSet[1].RepositoryID = o.CommitSet[0].RepositoryID
		}, true},
		"short sha":               {func(o *v1.PreviewDeployOperation) { o.CommitSet[0].CommitSHA = "1234" }, true},
		"commit set digest":       {func(o *v1.PreviewDeployOperation) { o.CommitSetDigest = strings.Repeat("e", 64) }, false},
		"blueprint id":            {func(o *v1.PreviewDeployOperation) { o.Blueprint.BlueprintID = "" }, true},
		"blueprint version":       {func(o *v1.PreviewDeployOperation) { o.Blueprint.Version = " " }, true},
		"blueprint digest":        {func(o *v1.PreviewDeployOperation) { o.Blueprint.Digest = strings.Repeat("e", 64) }, true},
		"blueprint content":       {func(o *v1.PreviewDeployOperation) { o.Blueprint.Content = nil }, true},
		"blueprint not object":    {func(o *v1.PreviewDeployOperation) { o.Blueprint.Content = json.RawMessage(`[]`) }, true},
		"blueprint content drift": {func(o *v1.PreviewDeployOperation) { o.Blueprint.Content = json.RawMessage(invalidContent) }, true},
		"blueprint invalid": {func(o *v1.PreviewDeployOperation) {
			o.Blueprint.Content, o.Blueprint.Digest = json.RawMessage(invalidContent), invalidDigest
		}, true},
		"blueprint oversize": {func(o *v1.PreviewDeployOperation) {
			o.Blueprint.Content = json.RawMessage(validPreviewDeployBlueprintContent + strings.Repeat(" ", 256<<10))
		}, true},
		"binding digest":   {func(o *v1.PreviewDeployOperation) { o.ResourceBindingDigest = strings.Repeat("e", 64) }, false},
		"operation digest": {func(o *v1.PreviewDeployOperation) { o.OperationDigest = strings.Repeat("e", 64) }, false},
		"command digest":   {func(o *v1.PreviewDeployOperation) { o.CommandDigest = strings.Repeat("e", 64) }, false},
		"tampered intent":  {func(o *v1.PreviewDeployOperation) { o.ExpiresAt = o.ExpiresAt.Add(time.Hour) }, false},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			operation := validPreviewDeployOperation()
			operation.CommitSet = append([]v1.PreviewObservationCommit(nil), operation.CommitSet...)
			test.mutate(&operation)
			if test.reseal {
				operation = sealPreviewDeployOperation(operation)
			}
			if err := PreviewDeployOperation(operation); err == nil {
				t.Fatal("invalid preview deploy operation accepted")
			}
		})
	}
}

func TestPreviewDeployCommandMessageID(t *testing.T) {
	if err := PreviewDeployCommandMessageID("preview-deploy-command-" + strings.Repeat("f", 64)); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"", "preview-lifecycle-command-" + strings.Repeat("f", 64), "preview-deploy-command-" + strings.Repeat("F", 64)} {
		if PreviewDeployCommandMessageID(value) == nil {
			t.Fatalf("command id %q accepted", value)
		}
	}
}

func validPreviewDeployResultEvent(outcome v1.PreviewDeployOutcome) v1.PreviewDeployResultEvent {
	operation := validPreviewDeployOperation()
	return v1.PreviewDeployResultEvent{
		Kind:                         v1.EventPreviewDeployResult,
		PreviewDeployResultAuthority: v1.PreviewDeployResultAuthorityFromOperation(operation),
		CommandMessageID:             "preview-deploy-command-" + strings.Repeat("f", 64),
		ObservedAt:                   operation.LeaseExpiresAt.Add(-time.Minute),
		Outcome:                      outcome,
	}
}

func TestPreviewDeployResultEventAcceptsOnlyNormalizedOutcomes(t *testing.T) {
	passed := []v1.PreviewObservationCheck{{CheckID: "home", Status: v1.PreviewObservationCheckPassed, HTTPStatus: 200, DurationMS: 12}}
	cases := map[string]struct {
		outcome  v1.PreviewDeployOutcome
		accepted bool
	}{
		"deployed":                {v1.PreviewDeployOutcome{Kind: v1.PreviewDeployDeployed, Checks: passed, LogTail: "ok\n"}, true},
		"deployed without checks": {v1.PreviewDeployOutcome{Kind: v1.PreviewDeployDeployed}, false},
		"deployed with phase":     {v1.PreviewDeployOutcome{Kind: v1.PreviewDeployDeployed, Checks: passed, Phase: v1.PreviewDeployPhaseBuild}, false},
		"deployed failed check": {v1.PreviewDeployOutcome{Kind: v1.PreviewDeployDeployed, Checks: []v1.PreviewObservationCheck{
			{CheckID: "home", Status: v1.PreviewObservationCheckFailed, HTTPStatus: 500, DurationMS: 12},
		}}, false},
		"unsorted checks": {v1.PreviewDeployOutcome{Kind: v1.PreviewDeployDeployed, Checks: []v1.PreviewObservationCheck{
			{CheckID: "z", Status: v1.PreviewObservationCheckPassed, HTTPStatus: 200},
			{CheckID: "a", Status: v1.PreviewObservationCheckPassed, HTTPStatus: 200},
		}}, false},
		"check status code": {v1.PreviewDeployOutcome{Kind: v1.PreviewDeployDeployed, Checks: []v1.PreviewObservationCheck{
			{CheckID: "home", Status: v1.PreviewObservationCheckPassed, HTTPStatus: 99},
		}}, false},
		"failed":                {v1.PreviewDeployOutcome{Kind: v1.PreviewDeployFailed, Phase: v1.PreviewDeployPhaseHealth, Checks: passed, LogTail: "x"}, true},
		"failed lease":          {v1.PreviewDeployOutcome{Kind: v1.PreviewDeployFailed, Phase: v1.PreviewDeployPhaseLeaseExpired}, true},
		"failed no phase":       {v1.PreviewDeployOutcome{Kind: v1.PreviewDeployFailed}, false},
		"failed unknown phase":  {v1.PreviewDeployOutcome{Kind: v1.PreviewDeployFailed, Phase: "deploy"}, false},
		"capacity":              {v1.PreviewDeployOutcome{Kind: v1.PreviewDeployCapacityExhausted}, true},
		"capacity with tail":    {v1.PreviewDeployOutcome{Kind: v1.PreviewDeployCapacityExhausted, LogTail: "x"}, false},
		"uncertain":             {v1.PreviewDeployOutcome{Kind: v1.PreviewDeployUncertain}, true},
		"uncertain with phase":  {v1.PreviewDeployOutcome{Kind: v1.PreviewDeployUncertain, Phase: v1.PreviewDeployPhaseBuild}, false},
		"unknown kind":          {v1.PreviewDeployOutcome{Kind: "succeeded"}, false},
		"tail at line cap":      {v1.PreviewDeployOutcome{Kind: v1.PreviewDeployFailed, Phase: v1.PreviewDeployPhaseBuild, LogTail: strings.Repeat("x\n", 200)}, true},
		"tail over line cap":    {v1.PreviewDeployOutcome{Kind: v1.PreviewDeployFailed, Phase: v1.PreviewDeployPhaseBuild, LogTail: strings.Repeat("x\n", 200) + "x"}, false},
		"tail at byte cap":      {v1.PreviewDeployOutcome{Kind: v1.PreviewDeployFailed, Phase: v1.PreviewDeployPhaseBuild, LogTail: strings.Repeat("x", 16<<10)}, true},
		"tail over byte cap":    {v1.PreviewDeployOutcome{Kind: v1.PreviewDeployFailed, Phase: v1.PreviewDeployPhaseBuild, LogTail: strings.Repeat("x", 16<<10+1)}, false},
		"tail invalid utf8":     {v1.PreviewDeployOutcome{Kind: v1.PreviewDeployFailed, Phase: v1.PreviewDeployPhaseBuild, LogTail: "\xff"}, false},
		"failed with bad check": {v1.PreviewDeployOutcome{Kind: v1.PreviewDeployFailed, Phase: v1.PreviewDeployPhaseHealth, Checks: []v1.PreviewObservationCheck{{CheckID: ""}}}, false},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			err := PreviewDeployResultEvent(validPreviewDeployResultEvent(test.outcome))
			if test.accepted && err != nil {
				t.Fatalf("valid result rejected: %v", err)
			}
			if !test.accepted && err == nil {
				t.Fatal("invalid result accepted")
			}
		})
	}
}

func TestPreviewDeployResultEventRejectsAuthorityDrift(t *testing.T) {
	outcome := v1.PreviewDeployOutcome{Kind: v1.PreviewDeployUncertain}
	cases := map[string]func(*v1.PreviewDeployResultEvent){
		"kind":           func(e *v1.PreviewDeployResultEvent) { e.Kind = v1.EventPreviewLifecycleResult },
		"command id":     func(e *v1.PreviewDeployResultEvent) { e.CommandMessageID = "command-1" },
		"observed zero":  func(e *v1.PreviewDeployResultEvent) { e.ObservedAt = time.Time{} },
		"observed late":  func(e *v1.PreviewDeployResultEvent) { e.ObservedAt = e.LeaseExpiresAt.Add(time.Millisecond) },
		"binding drift":  func(e *v1.PreviewDeployResultEvent) { e.PreviewKey = "zzzzzzzzzzzzzzzzzzzzzzzzzz" },
		"blueprint":      func(e *v1.PreviewDeployResultEvent) { e.Blueprint.Version = "2" },
		"command digest": func(e *v1.PreviewDeployResultEvent) { e.FencingToken = 5 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			event := validPreviewDeployResultEvent(outcome)
			mutate(&event)
			if PreviewDeployResultEvent(event) == nil {
				t.Fatal("drifted result accepted")
			}
		})
	}
}

func validPreviewHostedLifecycleOperation(action v1.PreviewLifecycleAction) v1.PreviewLifecycleOperation {
	operation := v1.PreviewLifecycleOperation{
		PreviewLifecycleOperationAuthority: v1.PreviewLifecycleOperationAuthority{
			SchemaVersion:                  v1.PreviewLifecycleSchemaVersion,
			PreviewLifecycleEffectID:       "preview-lifecycle-effect-1",
			PreviewLifecycleEffectRevision: 2,
			PreviewLifecycleOperationID:    "preview-lifecycle-operation-" + strings.Repeat("a", 64),
			OrganizationID:                 "organization-1", ProjectID: "project-1", WorkflowRunID: "workflow-1",
			ExecutionManifestID: "manifest-1", ExecutionManifestDigest: strings.Repeat("c", 64),
			Action: action, IdempotencyKey: "preview-lifecycle-" + strings.Repeat("d", 64),
			LeaseID: "lease-1", FencingToken: 4, LeaseExpiresAt: time.Date(2030, 8, 14, 12, 45, 0, 0, time.UTC),
		},
		PreviewID:  "preview-1",
		PreviewKey: "abcdefghijklmnopqrstuvwxyz",
		Blueprint: v1.PreviewObservationBlueprint{
			BlueprintID: "node-web", Version: "1",
			Digest: "83ded9d685a2c763c73dd2ea0f56a054fe1e851b8b44c8307211ea518fa60563",
		},
		CommitSet: []v1.PreviewObservationCommit{
			{RepositoryID: "api", CommitSHA: strings.Repeat("1", 40)},
			{RepositoryID: "web", CommitSHA: strings.Repeat("2", 40)},
		},
		OriginalExpiresAt:         time.Date(2030, 8, 14, 18, 0, 0, 0, time.UTC),
		CurrentEffectiveExpiresAt: time.Date(2030, 8, 14, 18, 0, 0, 0, time.UTC),
	}
	if action == v1.PreviewLifecycleExtend {
		desired := time.Date(2030, 8, 15, 18, 0, 0, 0, time.UTC)
		operation.DesiredExpiresAt = &desired
	}
	return sealPreviewHostedLifecycleOperation(operation)
}

func sealPreviewHostedLifecycleOperation(operation v1.PreviewLifecycleOperation) v1.PreviewLifecycleOperation {
	operation.CommitSetDigest = v1.PreviewLifecycleCommitSetDigest(operation.CommitSet)
	operation.ResourceBindingDigest = v1.PreviewHostedLifecycleResourceBindingDigest(operation)
	operation.OperationDigest = v1.PreviewHostedLifecycleOperationDigest(operation)
	operation.CommandDigest = v1.PreviewLifecycleCommandDigest(operation)
	return operation
}

func TestPreviewHostedLifecycleOperationAcceptsBothActions(t *testing.T) {
	for _, action := range []v1.PreviewLifecycleAction{v1.PreviewLifecycleExtend, v1.PreviewLifecycleDestroy} {
		operation := validPreviewHostedLifecycleOperation(action)
		if err := PreviewHostedLifecycleOperation(operation); err != nil {
			t.Fatalf("%s rejected: %v", action, err)
		}
		if PreviewLifecycleOperation(operation) == nil {
			t.Fatalf("external validator accepted hosted %s", action)
		}
	}
	if PreviewHostedLifecycleOperation(validPreviewLifecycleOperation(v1.PreviewLifecycleExtend)) == nil {
		t.Fatal("hosted validator accepted external operation")
	}
}

func TestPreviewHostedLifecycleOperationRejectsExternalTargetFields(t *testing.T) {
	cases := map[string]func(*v1.PreviewLifecycleOperation){
		"evidence":           func(o *v1.PreviewLifecycleOperation) { o.PreviewEvidenceID = "preview-evidence-1" },
		"observation":        func(o *v1.PreviewLifecycleOperation) { o.PreviewObservationID = "preview-observation-1" },
		"observation digest": func(o *v1.PreviewLifecycleOperation) { o.PreviewObservationDigest = strings.Repeat("2", 64) },
		"url":                func(o *v1.PreviewLifecycleOperation) { o.PreviewURL = "https://preview.example.test/" },
		"preview key":        func(o *v1.PreviewLifecycleOperation) { o.PreviewKey = "ABCDEFGHIJKLMNOPQRSTUVWXYZ" },
		"preview id":         func(o *v1.PreviewLifecycleOperation) { o.PreviewID = "bad id" },
		"no desired":         func(o *v1.PreviewLifecycleOperation) { o.DesiredExpiresAt = nil },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			operation := validPreviewHostedLifecycleOperation(v1.PreviewLifecycleExtend)
			mutate(&operation)
			if PreviewHostedLifecycleOperation(sealPreviewHostedLifecycleOperation(operation)) == nil {
				t.Fatal("invalid hosted operation accepted")
			}
		})
	}
	digests := map[string]func(*v1.PreviewLifecycleOperation){
		"binding":   func(o *v1.PreviewLifecycleOperation) { o.ResourceBindingDigest = strings.Repeat("e", 64) },
		"operation": func(o *v1.PreviewLifecycleOperation) { o.OperationDigest = strings.Repeat("e", 64) },
		"command":   func(o *v1.PreviewLifecycleOperation) { o.CommandDigest = strings.Repeat("e", 64) },
		"external binding": func(o *v1.PreviewLifecycleOperation) {
			o.ResourceBindingDigest = v1.PreviewLifecycleResourceBindingDigest(*o)
		},
	}
	for name, mutate := range digests {
		t.Run(name, func(t *testing.T) {
			operation := validPreviewHostedLifecycleOperation(v1.PreviewLifecycleExtend)
			mutate(&operation)
			if PreviewHostedLifecycleOperation(operation) == nil {
				t.Fatal("hosted digest drift accepted")
			}
		})
	}
}

func TestHostedPreviewBlueprintVersionIsAPositiveDecimalInteger(t *testing.T) {
	versions := map[string]bool{
		"1": true, "42": true, "9999999999999999": true,
		"": false, "0": false, "01": false, "-1": false, "+1": false, "1.0": false, "1e3": false,
		" 1": false, "1 ": false, "2026.08.1": false, "10000000000000000": false, "١": false,
	}
	for version, valid := range versions {
		t.Run(version, func(t *testing.T) {
			deploy := validPreviewDeployOperation()
			deploy.CommitSet = append([]v1.PreviewObservationCommit(nil), deploy.CommitSet...)
			deploy.Blueprint.Version = version
			if err := PreviewDeployOperation(sealPreviewDeployOperation(deploy)); (err == nil) != valid {
				t.Fatalf("deploy version %q err=%v", version, err)
			}
			for _, action := range []v1.PreviewLifecycleAction{v1.PreviewLifecycleExtend, v1.PreviewLifecycleDestroy} {
				lifecycle := validPreviewHostedLifecycleOperation(action)
				lifecycle.Blueprint.Version = version
				if err := PreviewHostedLifecycleOperation(sealPreviewHostedLifecycleOperation(lifecycle)); (err == nil) != valid {
					t.Fatalf("hosted %s version %q err=%v", action, version, err)
				}
			}
		})
	}
}

func TestPreviewHostedLifecycleResultEvent(t *testing.T) {
	extend := validPreviewHostedLifecycleOperation(v1.PreviewLifecycleExtend)
	destroy := validPreviewHostedLifecycleOperation(v1.PreviewLifecycleDestroy)
	blueprint := extend.Blueprint
	event := func(operation v1.PreviewLifecycleOperation, outcome v1.PreviewLifecycleResultOutcome) v1.PreviewLifecycleResultEvent {
		return v1.PreviewLifecycleResultEvent{
			Kind:                            v1.EventPreviewLifecycleResult,
			PreviewLifecycleResultAuthority: v1.PreviewLifecycleResultAuthorityFromOperation(operation),
			CommandMessageID:                "preview-lifecycle-command-" + strings.Repeat("f", 64),
			ObservedAt:                      operation.LeaseExpiresAt.Add(-time.Minute),
			Outcome:                         outcome,
		}
	}
	extended := v1.PreviewLifecycleResultOutcome{
		Kind: v1.PreviewLifecycleSucceeded, ControllerReceiptDigest: strings.Repeat("9", 64),
		ResourceBindingDigest: extend.ResourceBindingDigest,
		ResourceState: &v1.PreviewLifecycleResourceState{
			Kind: v1.PreviewLifecycleResourceActive, Blueprint: &blueprint,
			CommitSet: extend.CommitSet, ExpiresAt: extend.DesiredExpiresAt,
		},
	}
	destroyed := v1.PreviewLifecycleResultOutcome{
		Kind: v1.PreviewLifecycleSucceeded, ControllerReceiptDigest: strings.Repeat("9", 64),
		ResourceBindingDigest: destroy.ResourceBindingDigest,
		ResourceState: &v1.PreviewLifecycleResourceState{
			Kind: v1.PreviewLifecycleResourceDestroyed, Disposition: v1.PreviewLifecycleAlreadyAbsent,
		},
	}
	if err := PreviewHostedLifecycleResultEvent(event(extend, extended)); err != nil {
		t.Fatalf("hosted extend result rejected: %v", err)
	}
	if err := PreviewHostedLifecycleResultEvent(event(destroy, destroyed)); err != nil {
		t.Fatalf("hosted destroy result rejected: %v", err)
	}
	if PreviewLifecycleResultEvent(event(destroy, destroyed)) == nil {
		t.Fatal("external result validator accepted hosted result")
	}
	withURL := extended
	state := *extended.ResourceState
	state.PreviewURL = "https://preview.example.test/"
	withURL.ResourceState = &state
	if PreviewHostedLifecycleResultEvent(event(extend, withURL)) == nil {
		t.Fatal("hosted result with preview url accepted")
	}
	uncertain := v1.PreviewLifecycleResultOutcome{Kind: v1.PreviewLifecycleUncertain, Code: v1.PreviewLifecycleControllerTimeout}
	if err := PreviewHostedLifecycleResultEvent(event(destroy, uncertain)); err != nil {
		t.Fatalf("hosted uncertain result rejected: %v", err)
	}
}
