package main

import (
	"bytes"
	"context"
	"reflect"
	"testing"

	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/doctor"
)

func TestDoctorCommandPassesOnlyTheChecksThatDeclareAnImage(t *testing.T) {
	t.Parallel()
	image := "our-runner-check-go:rdbc@sha256:" + "abcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcd"
	cfg := config.Default()
	cfg.ControlPlaneURL = "https://control.example"
	cfg.Checks = map[string]config.CheckConfig{
		"go-test": {Command: "go", Args: []string{"test", "./..."}, Image: image},
		"lint":    {Command: "golangci-lint"},
	}
	var captured doctor.Options
	var output bytes.Buffer
	err := cmdDoctorWithDependencies([]string{"--config", "operator.yaml"}, doctorCommandDependencies{
		load: func(string) (config.Config, error) { return cfg, nil },
		run: func(_ context.Context, opts doctor.Options) doctor.Report {
			captured = opts
			return doctor.Report{}
		},
		output: &output,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(captured.CheckImages, map[string]string{"go-test": image}) {
		t.Fatalf("check images = %+v", captured.CheckImages)
	}
	cfg.Checks = map[string]config.CheckConfig{"lint": {Command: "golangci-lint"}}
	if opts := loadedConfigurationDoctorOptions(cfg); len(opts.CheckImages) != 0 {
		t.Fatalf("check images without a declared image = %+v", opts.CheckImages)
	}
}
