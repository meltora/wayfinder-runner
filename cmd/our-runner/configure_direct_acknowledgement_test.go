package main

import (
	"net/http"
	"strings"
	"testing"
)

func TestConfigureStoresTheDirectAcknowledgementOnlyForADirectDelivery(t *testing.T) {
	h := newConfigureHarness(t)
	h.respond(http.StatusOK, configureDelivery(t, h.runnerID, "docker"))
	if err := h.run("", false, "--non-interactive", "--acknowledge-direct"); err != nil {
		t.Fatal(err)
	}
	if h.local(t).AcknowledgeDirect {
		t.Fatal("acknowledgement stored for a docker delivery")
	}
	h.respond(http.StatusOK, configureDelivery(t, h.runnerID, "direct"))
	if err := h.run("", false, "--non-interactive"); err == nil || !strings.Contains(err.Error(), "--acknowledge-direct") {
		t.Fatalf("docker to direct switch err = %v", err)
	}
}

func TestConfigureWithdrawsAnEarlierDirectAcknowledgementWhenTheDashboardDeliversDocker(t *testing.T) {
	h := newConfigureHarness(t)
	h.respond(http.StatusOK, configureDelivery(t, h.runnerID, "direct"))
	if err := h.run("", false, "--non-interactive", "--acknowledge-direct"); err != nil {
		t.Fatal(err)
	}
	if !h.local(t).AcknowledgeDirect {
		t.Fatal("acknowledgement not recorded")
	}
	h.respond(http.StatusOK, configureDelivery(t, h.runnerID, "docker"))
	if err := h.run("", false, "--non-interactive"); err != nil {
		t.Fatal(err)
	}
	if h.local(t).AcknowledgeDirect {
		t.Fatal("acknowledgement kept after a docker delivery")
	}
	h.respond(http.StatusOK, configureDelivery(t, h.runnerID, "direct"))
	if err := h.run("", false, "--non-interactive"); err == nil || !strings.Contains(err.Error(), "--acknowledge-direct") {
		t.Fatalf("docker to direct switch err = %v", err)
	}
}
