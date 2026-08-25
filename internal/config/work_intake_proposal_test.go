package config

import (
	"runtime"
	"testing"
)

func TestWorkIntakeProposalConsultationRequiresExplicitCompleteOptIn(t *testing.T) {
	cfg := Default()
	cfg.ControlPlaneURL = "wss://control.example.com/runner"
	cfg.JobImage = "registry.example.com/runner/job@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	cfg.JobNetwork = "restricted"
	cfg.JobNetworkName = "provider-egress"
	cfg.EgressPolicyID = "consultation-v1"
	if cfg.WorkIntakeProposal.Enabled {
		t.Fatal("consultation must default disabled")
	}
	for name, value := range map[string]WorkIntakeProposalConfig{
		"enabled complete": {Enabled: true, Adapter: "codex", Model: "consultation-model"},
		"disabled empty":   {},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := cfg
			candidate.WorkIntakeProposal = value
			err := candidate.Validate()
			if value.Enabled && runtime.GOOS != "linux" {
				if err == nil {
					t.Fatal("consultation accepted without Linux kernel-authenticated policy leases")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
		})
	}
	for name, value := range map[string]WorkIntakeProposalConfig{
		"missing adapter":  {Enabled: true, Model: "consultation-model"},
		"missing model":    {Enabled: true, Adapter: "codex"},
		"unknown adapter":  {Enabled: true, Adapter: "other", Model: "consultation-model"},
		"unsafe model":     {Enabled: true, Adapter: "codex", Model: "model\nother"},
		"disabled binding": {Enabled: false, Adapter: "codex", Model: "consultation-model"},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := cfg
			candidate.WorkIntakeProposal = value
			if err := candidate.Validate(); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
	direct := cfg
	direct.Isolation = "direct"
	direct.AllowUnsafeDirect = true
	direct.WorkIntakeProposal = WorkIntakeProposalConfig{Enabled: true, Adapter: "codex", Model: "consultation-model"}
	if err := direct.Validate(); err == nil {
		t.Fatal("consultation accepted without hardened Docker isolation")
	}
	noNetwork := cfg
	noNetwork.JobNetwork = "none"
	noNetwork.JobNetworkName = ""
	noNetwork.EgressPolicyID = ""
	noNetwork.WorkIntakeProposal = WorkIntakeProposalConfig{Enabled: true, Adapter: "codex", Model: "consultation-model"}
	if err := noNetwork.Validate(); err == nil {
		t.Fatal("remote consultation accepted without approved provider egress")
	}
}

func TestWorkIntakeProposalConsultationIsLinuxOnly(t *testing.T) {
	if !workIntakeProposalSupportedOS("linux") || workIntakeProposalSupportedOS("darwin") ||
		workIntakeProposalSupportedOS("windows") {
		t.Fatal("consultation platform gate is not Linux-only")
	}
}
