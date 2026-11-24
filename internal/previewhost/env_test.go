package previewhost

import (
	"context"
	"regexp"
	"strings"
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/redact"
)

func TestGeneratedEnvironmentIsConsistentAcrossContainers(t *testing.T) {
	h := newHarness(t)
	if outcome := h.deploy(context.Background(), testContent(), testOperation()); outcome.Kind != v1.PreviewDeployDeployed {
		t.Fatalf("outcome = %+v", outcome)
	}
	prefix := ResourcePrefix(testRunnerID, testPreviewID)
	created := h.engine.Created()
	db := specNamed(t, created, prefix+"-svc-db").Env
	app := specNamed(t, created, prefix+"-app").Env
	password := db["POSTGRES_PASSWORD"]
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(password) || db["POSTGRES_USER"] != "preview" {
		t.Fatalf("database env = %v", db)
	}
	if app["POSTGRES_PASSWORD"] != password {
		t.Fatal("the same generated key resolved to two values")
	}
	if app["DATABASE_URL"] != "postgres://preview:"+password+"@db:5432/preview" {
		t.Fatalf("DATABASE_URL = %q", app["DATABASE_URL"])
	}
	if app["SESSION_SECRET"] == password || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(app["SESSION_SECRET"]) {
		t.Fatalf("SESSION_SECRET = %q", app["SESSION_SECRET"])
	}
	if app["REDIS_URL"] != "redis://cache:6379" || app["SMTP_URL"] != "smtp://mail:1025" ||
		app["PUBLIC_URL"] != "http://127.0.0.1:3000" || app["FEATURE_FLAG"] != "on" {
		t.Fatalf("app env = %v", app)
	}
	for _, unset := range []string{"STRIPE_KEY", "ANALYTICS_TOKEN"} {
		if _, present := app[unset]; present {
			t.Fatalf("%s is set", unset)
		}
	}
	for _, spec := range created {
		if strings.Contains(spec.Name, "-run-") && spec.Env["DATABASE_URL"] != app["DATABASE_URL"] {
			t.Fatalf("phase container %s has a different environment", spec.Name)
		}
	}
	if redact.KnownSecrets(password) == password || redact.KnownSecrets(app["SESSION_SECRET"]) == app["SESSION_SECRET"] {
		t.Fatal("generated secrets are not redacted")
	}
}

func TestGeneratedEnvironmentDefaultsAndEdgeHost(t *testing.T) {
	redact.ForgetSecretsForTest()
	t.Cleanup(redact.ForgetSecretsForTest)
	content := testContent()
	content.Services[0].Env = map[string]v1.PreviewBlueprintEnvEntryV1{"POSTGRES_PASSWORD": fake("p@ss/word")}
	operation := testOperation()
	operation.EdgeHost = strings.Repeat("a", 26) + ".preview.example.com"
	env, err := resolveEnvironment(content, operation, func() (string, error) { return strings.Repeat("c", 64), nil })
	if err != nil {
		t.Fatal(err)
	}
	if got := env.app["DATABASE_URL"]; got != "postgres://postgres:p%40ss%2Fword@db:5432/postgres" {
		t.Fatalf("DATABASE_URL = %q", got)
	}
	if got := env.app["PUBLIC_URL"]; got != "https://"+operation.EdgeHost {
		t.Fatalf("PUBLIC_URL = %q", got)
	}
	if env.services["db"]["POSTGRES_PASSWORD"] != "p@ss/word" {
		t.Fatalf("db env = %v", env.services["db"])
	}
}
