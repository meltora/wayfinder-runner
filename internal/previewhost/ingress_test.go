package previewhost

import (
	"context"
	"net"
	"slices"
	"strconv"
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func TestIngressAddressIsTheLoopbackPortOfTheGatewayIngress(t *testing.T) {
	h := newHarness(t)
	if outcome := h.deploy(context.Background(), testContent(), testOperation()); outcome.Kind != v1.PreviewDeployDeployed {
		t.Fatalf("outcome = %+v", outcome)
	}
	address, err := h.controller.IngressAddress(context.Background(), testPreviewID)
	if err != nil {
		t.Fatal(err)
	}
	if want := net.JoinHostPort("127.0.0.1", strconv.Itoa(h.engine.hostPort)); address != want {
		t.Fatalf("address = %q, want %q", address, want)
	}
	gateway := gatewayName(ResourcePrefix(testRunnerID, testPreviewID)) + ":" + strconv.Itoa(gatewayIngressPort)
	if calls := h.engine.HostPortCalls(); calls[len(calls)-1] != gateway {
		t.Fatalf("host port calls = %v", calls)
	}
}

func TestIngressAddressFailsForAPreviewWithoutAGateway(t *testing.T) {
	h := newHarness(t)
	controller := h.build()
	if address, err := controller.IngressAddress(context.Background(), "preview-missing"); err == nil {
		t.Fatalf("address = %q, want an error", address)
	}
	want := gatewayName(ResourcePrefix(testRunnerID, "preview-missing")) + ":" + strconv.Itoa(gatewayIngressPort)
	if calls := h.engine.HostPortCalls(); !slices.Equal(calls, []string{want}) {
		t.Fatalf("host port calls = %v", calls)
	}
}
