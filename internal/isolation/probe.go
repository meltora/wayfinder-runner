package isolation

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"time"
)

type DockerInfo struct {
	Available     bool   `json:"available"`
	Rootless      bool   `json:"rootless"`
	UsernsRemap   bool   `json:"userns_remap"`
	ServerVersion string `json:"server_version,omitempty"`
	Driver        string `json:"driver,omitempty"`
}

type dockerInfoProbe struct {
	SecurityOptions []string `json:"SecurityOptions"`
	ServerVersion   string   `json:"ServerVersion"`
	Driver          string   `json:"Driver"`
}

func ProbeDocker(ctx context.Context) DockerInfo {
	return probeDocker(ctx, func(ctx context.Context) ([]byte, error) {
		c, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		return exec.CommandContext(c, "docker", "info", "--format", "{{json .}}").Output()
	})
}

func probeDocker(ctx context.Context, run func(context.Context) ([]byte, error)) DockerInfo {
	var info DockerInfo
	out, err := run(ctx)
	if err != nil {
		return info
	}
	var probe dockerInfoProbe
	if err := json.Unmarshal(out, &probe); err != nil {
		return info
	}
	info.Available = true
	info.ServerVersion = probe.ServerVersion
	info.Driver = probe.Driver
	for _, opt := range probe.SecurityOptions {
		if opt == "rootless" || strings.HasSuffix(opt, "=rootless") {
			info.Rootless = true
		}
		if strings.Contains(opt, "userns") {
			info.UsernsRemap = true
		}
	}
	return info
}
