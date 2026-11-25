package previewhost

import (
	"context"
	"maps"
	"net/netip"
	"slices"
	"strings"
	"testing"

	v1 "github.com/example/app-runner/api/runner/v1"
)

const (
	testAttemptID    = "attempt-1"
	testJobContainer = "our-runner-job-container"
)

func addJobContainer(h *harness, name string) {
	h.engine.mu.Lock()
	defer h.engine.mu.Unlock()
	h.engine.containers[name] = ContainerSpec{Name: name}
}

func attachedHarness(t *testing.T) *harness {
	t.Helper()
	h := deployedHarness(t, testContent())
	addJobContainer(h, testJobContainer)
	if err := h.controller.AttachJob(context.Background(), testPreviewID, testAttemptID, testJobContainer); err != nil {
		t.Fatal(err)
	}
	return h
}

func TestJobNetworkNameIsStable(t *testing.T) {
	name := JobNetworkName(testRunnerID, testAttemptID)
	if name != "our-preview-job-0787555948ce" {
		t.Fatalf("name = %q", name)
	}
	if JobNetworkName(testRunnerID, "attempt-2") == name || JobNetworkName("runner-2", testAttemptID) == name {
		t.Fatal("job network name ignores its inputs")
	}
	if !dockerObjectNamePattern.MatchString(name) || strings.Contains(name, testAttemptID) {
		t.Fatalf("name = %q", name)
	}
}

func TestJobNetworkHasNoPreviewLabels(t *testing.T) {
	h := attachedHarness(t)
	name := JobNetworkName(testRunnerID, testAttemptID)
	spec, exists := h.engine.Networks()[name]
	if !exists {
		t.Fatalf("job network %s was not created", name)
	}
	if !maps.Equal(spec.Labels, map[string]string{
		"com.ourplatform.preview-job-network": testRunnerID, "com.ourplatform.attempt-id": testAttemptID,
	}) {
		t.Fatalf("labels = %v", spec.Labels)
	}
	if _, set := spec.Labels[LabelPreviewHost]; set {
		t.Fatalf("labels = %v", spec.Labels)
	}
	if _, set := spec.Labels[LabelPreviewID]; set {
		t.Fatalf("labels = %v", spec.Labels)
	}
	if !spec.Internal || !maps.Equal(spec.Options, map[string]string{
		"com.docker.network.bridge.inhibit_ipv4": "true", "com.docker.network.enable_ipv6": "false",
	}) {
		t.Fatalf("job network = %+v", spec)
	}
	objects, err := h.engine.List(context.Background(), PreviewLabels(testRunnerID, testPreviewID))
	if err != nil || slices.Contains(objects.Networks, name) {
		t.Fatalf("preview listing = %+v %v", objects, err)
	}
	ids, err := h.controller.HostedPreviewIDs(context.Background())
	if err != nil || !slices.Equal(ids, []string{testPreviewID}) {
		t.Fatalf("hosted previews = %v %v", ids, err)
	}
}

func TestAttachJobConnectsContainerAndGatewayAlias(t *testing.T) {
	h := attachedHarness(t)
	name := JobNetworkName(testRunnerID, testAttemptID)
	gateway := ResourcePrefix(testRunnerID, testPreviewID) + "-gateway"
	var job []fakeConnect
	for _, connect := range h.engine.Connects() {
		if connect.network == name {
			job = append(job, connect)
		}
	}
	want := []fakeConnect{
		{network: name, container: testJobContainer, alias: ""},
		{network: name, container: gateway, alias: "our-preview"},
	}
	if !slices.Equal(job, want) {
		t.Fatalf("job network connects = %+v", job)
	}
	if endpoints := h.engine.Endpoints(name); !slices.Equal(endpoints, []string{testJobContainer, gateway}) {
		t.Fatalf("endpoints = %v", endpoints)
	}

	missing := deployedHarness(t, testContent())
	addJobContainer(missing, testJobContainer)
	if err := missing.controller.AttachJob(context.Background(), "preview-absent", testAttemptID, testJobContainer); err == nil {
		t.Fatal("attach to a preview without a gateway succeeded")
	}
	if _, exists := missing.engine.Networks()[name]; exists {
		t.Fatal("job network survived a failed attach")
	}
	for _, args := range [][3]string{{"", testAttemptID, testJobContainer}, {testPreviewID, "", testJobContainer}, {testPreviewID, testAttemptID, ""}} {
		if err := missing.controller.AttachJob(context.Background(), args[0], args[1], args[2]); err == nil {
			t.Fatalf("attach %q accepted", args)
		}
	}
}

func TestAttachJobRejectsInvalidJobContainer(t *testing.T) {
	h := deployedHarness(t, testContent())
	before := len(h.engine.Events())
	for _, container := range []string{"", "-rm", "--force", "job container", "job/container", "." + testJobContainer, strings.Repeat("a", 129)} {
		if err := h.controller.AttachJob(context.Background(), testPreviewID, testAttemptID, container); err == nil {
			t.Fatalf("job container %q accepted", container)
		}
	}
	if events := h.engine.Events(); len(events) != before {
		t.Fatalf("invalid job container reached the engine: %v", events[before:])
	}
	if _, exists := h.engine.Networks()[JobNetworkName(testRunnerID, testAttemptID)]; exists {
		t.Fatal("job network created for an invalid job container")
	}
	addJobContainer(h, "0123456789abcdef0123")
	if err := h.controller.AttachJob(context.Background(), testPreviewID, testAttemptID, "0123456789abcdef0123"); err != nil {
		t.Fatalf("container id rejected: %v", err)
	}
}

func TestJobNetworkIsRemovedAtJobEndAndOnReplace(t *testing.T) {
	h := attachedHarness(t)
	name := JobNetworkName(testRunnerID, testAttemptID)
	second := testOperation()
	second.PreviewID = "preview-2"
	if outcome := h.deploy(context.Background(), testContent(), second); outcome.Kind != v1.PreviewDeployDeployed {
		t.Fatalf("outcome = %+v", outcome)
	}
	if err := h.controller.AttachJob(context.Background(), second.PreviewID, testAttemptID, testJobContainer); err != nil {
		t.Fatal(err)
	}
	events := h.engine.Events()
	first := ResourcePrefix(testRunnerID, testPreviewID) + "-gateway"
	replacement := ResourcePrefix(testRunnerID, second.PreviewID) + "-gateway"
	order := []string{
		"disconnect " + name + " " + testJobContainer + " true",
		"disconnect " + name + " " + first + " true",
		"rmnet " + name,
		"network " + name,
		"connect " + name + " " + testJobContainer + " ",
		"connect " + name + " " + replacement + " our-preview",
	}
	previous := phaseIndex(t, events, "connect "+name+" "+first+" our-preview")
	for _, event := range order {
		index := -1
		for candidate := previous + 1; candidate < len(events); candidate++ {
			if events[candidate] == event {
				index = candidate
				break
			}
		}
		if index < 0 {
			t.Fatalf("event %q missing after index %d in %v", event, previous, events)
		}
		previous = index
	}
	if endpoints := h.engine.Endpoints(name); !slices.Equal(endpoints, []string{testJobContainer, replacement}) {
		t.Fatalf("endpoints after replace = %v", endpoints)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := h.controller.DetachJob(ctx, testAttemptID); err != nil {
		t.Fatal(err)
	}
	if _, exists := h.engine.Networks()[name]; exists {
		t.Fatal("job network survived job end")
	}
	if err := h.controller.DetachJob(context.Background(), testAttemptID); err != nil {
		t.Fatalf("second detach: %v", err)
	}
	if err := h.controller.DetachJob(context.Background(), ""); err == nil {
		t.Fatal("detach without an attempt id succeeded")
	}
	for _, preview := range []string{testPreviewID, second.PreviewID} {
		objects, err := h.engine.List(context.Background(), PreviewLabels(testRunnerID, preview))
		if err != nil || objects.Empty() {
			t.Fatalf("detach touched preview %s: %+v %v", preview, objects, err)
		}
	}
}

func TestSweepJobNetworksKeepsActiveAttempt(t *testing.T) {
	h := attachedHarness(t)
	addJobContainer(h, "our-runner-other-job")
	if err := h.controller.AttachJob(context.Background(), testPreviewID, "attempt-2", "our-runner-other-job"); err != nil {
		t.Fatal(err)
	}
	foreign := NetworkSpec{Name: JobNetworkName("runner-other", "attempt-3"), Labels: JobNetworkLabels("runner-other", "attempt-3"), Internal: true}
	if err := h.engine.CreateNetwork(context.Background(), foreign); err != nil {
		t.Fatal(err)
	}
	unlabelled := NetworkSpec{Name: "our-preview-job-ffffffffffff", Labels: map[string]string{LabelPreviewJobNetwork: testRunnerID}, Internal: true}
	if err := h.engine.CreateNetwork(context.Background(), unlabelled); err != nil {
		t.Fatal(err)
	}
	if err := h.controller.SweepJobNetworks(context.Background(), func(attemptID string) bool { return attemptID == "attempt-2" }); err != nil {
		t.Fatal(err)
	}
	networks := h.engine.Networks()
	for name, want := range map[string]bool{
		JobNetworkName(testRunnerID, testAttemptID): false,
		JobNetworkName(testRunnerID, "attempt-2"):   true,
		foreign.Name:    true,
		unlabelled.Name: false,
	} {
		if _, exists := networks[name]; exists != want {
			t.Fatalf("network %s exists = %v, want %v", name, exists, want)
		}
	}
	if objects, err := h.engine.List(context.Background(), PreviewLabels(testRunnerID, testPreviewID)); err != nil || len(objects.Networks) != 2 {
		t.Fatalf("sweep touched preview networks: %+v %v", objects, err)
	}
	if err := h.controller.SweepJobNetworks(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if _, exists := h.engine.Networks()[JobNetworkName(testRunnerID, "attempt-2")]; exists {
		t.Fatal("sweep with no keeper left a job network")
	}
}

func TestTeardownIgnoresJobNetwork(t *testing.T) {
	h := attachedHarness(t)
	name := JobNetworkName(testRunnerID, testAttemptID)
	destroy := v1.PreviewLifecycleOperation{PreviewID: testPreviewID}
	destroy.Action = v1.PreviewLifecycleDestroy
	result := h.controller.ApplyLifecycle(context.Background(), LifecycleRequest{Operation: destroy}).Outcome
	if result.Kind != v1.PreviewLifecycleSucceeded {
		t.Fatalf("destroy = %+v", result)
	}
	if objects, err := h.engine.List(context.Background(), PreviewLabels(testRunnerID, testPreviewID)); err != nil || !objects.Empty() {
		t.Fatalf("preview left after destroy: %+v %v", objects, err)
	}
	if _, exists := h.engine.Networks()[name]; !exists {
		t.Fatal("preview teardown removed the job network")
	}
	if endpoints := h.engine.Endpoints(name); !slices.Equal(endpoints, []string{testJobContainer}) {
		t.Fatalf("job network endpoints after teardown = %v", endpoints)
	}
	if ids, err := h.controller.HostedPreviewIDs(context.Background()); err != nil || len(ids) != 0 {
		t.Fatalf("hosted previews = %v %v", ids, err)
	}
	if err := h.controller.DetachJob(context.Background(), testAttemptID); err != nil {
		t.Fatal(err)
	}
	if _, exists := h.engine.Networks()[name]; exists {
		t.Fatal("job network survived detach")
	}
}

func TestGatewaySpecPassesEgressSources(t *testing.T) {
	prefix := ResourcePrefix(testRunnerID, testPreviewID)
	h := newHarness(t)
	h.engine.subnets[prefix] = []netip.Prefix{netip.MustParsePrefix("172.25.0.0/16"), netip.MustParsePrefix("fd00:9::/64")}
	if outcome := h.deploy(context.Background(), allowContent(), testOperation()); outcome.Kind != v1.PreviewDeployDeployed {
		t.Fatalf("outcome = %+v", outcome)
	}
	gateway := specNamed(t, h.engine.Created(), prefix+"-gateway")
	var sources []string
	for index, arg := range gateway.Command {
		if arg == "--egress-source" && index+1 < len(gateway.Command) {
			sources = append(sources, gateway.Command[index+1])
		}
	}
	if !slices.Equal(sources, []string{"172.25.0.0/16", "fd00:9::/64"}) {
		t.Fatalf("gateway command = %q", gateway.Command)
	}
	events := h.engine.Events()
	if phaseIndex(t, events, "network "+prefix) >= phaseIndex(t, events, "subnets "+prefix) ||
		phaseIndex(t, events, "subnets "+prefix) >= phaseIndex(t, events, "detached "+prefix+"-gateway") {
		t.Fatalf("subnets not read between network creation and gateway start: %v", events)
	}

	none := deployedHarness(t, testContent())
	if slices.Contains(specNamed(t, none.engine.Created(), prefix+"-gateway").Command, "--egress-source") {
		t.Fatal("egress sources passed without an egress listener")
	}
	for _, event := range none.engine.Events() {
		if strings.HasPrefix(event, "subnets ") {
			t.Fatalf("subnets read in egress mode none: %v", none.engine.Events())
		}
	}

	empty := newHarness(t)
	empty.engine.subnets[prefix] = nil
	outcome := empty.deploy(context.Background(), allowContent(), testOperation())
	if outcome.Kind != v1.PreviewDeployFailed || outcome.Phase != v1.PreviewDeployPhaseServices {
		t.Fatalf("deploy without a preview subnet = %+v", outcome)
	}
	for _, spec := range empty.engine.Created() {
		if spec.Name == prefix+"-gateway" {
			t.Fatal("gateway started without egress sources")
		}
	}
}
