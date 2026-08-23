package config

import (
	"strings"
	"testing"
)

func validPreviewLifecycleController() PreviewLifecycleControllerConfig {
	return PreviewLifecycleControllerConfig{
		BlueprintID: "node-web-app", Version: "2026.08.23", Digest: strings.Repeat("a", 64),
		ControllerURL: "https://preview-controller.example.com/v1/preview-lifecycle",
		Actions:       []string{"destroy", "extend"}, TimeoutSeconds: 20, MaxResponseBytes: 64 << 10,
		MaxExtensionMS: uint64(30 * 24 * 60 * 60 * 1000),
	}
}

func TestPreviewLifecycleControllerAcceptsOnlyLocalBoundedHTTPSConfiguration(t *testing.T) {
	cfg := productionConfig()
	cfg.PreviewLifecycleControllers = map[string]PreviewLifecycleControllerConfig{
		"node-web": validPreviewLifecycleController(),
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid preview lifecycle controller rejected: %v", err)
	}
}

func TestPreviewLifecycleControllerRejectsUnsafeConfiguration(t *testing.T) {
	tests := map[string]func(*PreviewLifecycleControllerConfig){
		"blueprint": func(value *PreviewLifecycleControllerConfig) { value.BlueprintID = "../foreign" },
		"digest":    func(value *PreviewLifecycleControllerConfig) { value.Digest = strings.Repeat("A", 64) },
		"http": func(value *PreviewLifecycleControllerConfig) {
			value.ControllerURL = "http://preview-controller.example.com/apply"
		},
		"credentials": func(value *PreviewLifecycleControllerConfig) {
			value.ControllerURL = "https://user:secret@preview-controller.example.com/apply"
		},
		"query":          func(value *PreviewLifecycleControllerConfig) { value.ControllerURL += "?token=secret" },
		"fragment":       func(value *PreviewLifecycleControllerConfig) { value.ControllerURL += "#fragment" },
		"relative CA":    func(value *PreviewLifecycleControllerConfig) { value.TrustedCAFile = "ca.pem" },
		"relative auth":  func(value *PreviewLifecycleControllerConfig) { value.AuthorizationFile = "token" },
		"actions order":  func(value *PreviewLifecycleControllerConfig) { value.Actions = []string{"extend", "destroy"} },
		"unknown action": func(value *PreviewLifecycleControllerConfig) { value.Actions = []string{"destroy", "shell"} },
		"timeout":        func(value *PreviewLifecycleControllerConfig) { value.TimeoutSeconds = 121 },
		"response":       func(value *PreviewLifecycleControllerConfig) { value.MaxResponseBytes = 2 << 20 },
		"extension":      func(value *PreviewLifecycleControllerConfig) { value.MaxExtensionMS++ },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			controller := validPreviewLifecycleController()
			mutate(&controller)
			cfg := productionConfig()
			cfg.PreviewLifecycleControllers = map[string]PreviewLifecycleControllerConfig{"node-web": controller}
			if err := cfg.Validate(); err == nil {
				t.Fatal("unsafe preview lifecycle controller accepted")
			}
		})
	}
}

func TestNormalizeDeepCopiesAndDefaultsPreviewLifecycleControllers(t *testing.T) {
	controller := validPreviewLifecycleController()
	controller.Actions = nil
	controller.TimeoutSeconds = 0
	controller.MaxResponseBytes = 0
	controller.MaxExtensionMS = 0
	in := Config{PreviewLifecycleControllers: map[string]PreviewLifecycleControllerConfig{"node-web": controller}}
	out := Normalize(in)
	copyValue := out.PreviewLifecycleControllers["node-web"]
	if strings.Join(copyValue.Actions, ",") != "destroy,extend" || copyValue.TimeoutSeconds != 20 || copyValue.MaxResponseBytes != 64<<10 || copyValue.MaxExtensionMS != uint64(30*24*60*60*1000) {
		t.Fatalf("defaults = %+v", copyValue)
	}
	copyValue.Actions[0] = "changed"
	if len(in.PreviewLifecycleControllers["node-web"].Actions) != 0 {
		t.Fatal("Normalize mutated caller preview lifecycle controller")
	}
}

func TestPreviewLifecycleControllersRejectAmbiguousBlueprintIdentity(t *testing.T) {
	controller := validPreviewLifecycleController()
	cfg := productionConfig()
	cfg.PreviewLifecycleControllers = map[string]PreviewLifecycleControllerConfig{
		"first": controller, "second": controller,
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("ambiguous preview lifecycle controller identity accepted")
	}
}
