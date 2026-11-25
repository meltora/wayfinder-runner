package previewhost

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"
)

const (
	missingTestNetwork   = "our-preview-0123456789ab"
	missingTestContainer = "0123456789abcdef"
	missingTestImage     = "our-preview-gateway:0123456789abcdef"
	missingTestOrphan    = "our-preview-0123456789ab-gateway"
	badDockerContext     = `Failed to initialize: unable to resolve docker endpoint: context "gone": context not found: open /home/runner/.docker/contexts/meta/0123/meta.json: no such file or directory`
)

func fakeDockerFailure(t *testing.T, output string) {
	t.Helper()
	installFakeDocker(t, "cat >&2 <<'EOF'\n"+output+"\nEOF\nexit 1")
}

func networkContainersJSON(entries ...[2]string) string {
	parts := make([]string, 0, len(entries))
	for _, entry := range entries {
		parts = append(parts, `"`+entry[0]+`":{"Name":"`+entry[1]+`","EndpointID":"`+strings.Repeat("cd34", 16)+`","MacAddress":"","IPv4Address":"","IPv6Address":""}`)
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func TestDockerCLINetworkContainersReportsOrphanedEndpointNames(t *testing.T) {
	orphan := "ep-" + strings.Repeat("ab12", 16)
	installFakeDocker(t, "echo '"+networkContainersJSON(
		[2]string{missingTestContainer, "our-runner-job-container"},
		[2]string{orphan, "our-preview-0123456789ab-gateway"},
		[2]string{"ep-89abcdef0123", "stale.endpoint_1"},
	)+"'")
	cli := DockerCLI{TempDir: privateDir(t)}
	ids, orphans, err := cli.NetworkContainers(context.Background(), missingTestNetwork)
	if err != nil || !slices.Equal(ids, []string{missingTestContainer}) {
		t.Fatalf("containers = %v %v", ids, err)
	}
	if !slices.Equal(orphans, []string{"our-preview-0123456789ab-gateway", "stale.endpoint_1"}) {
		t.Fatalf("orphans = %v", orphans)
	}
	for _, empty := range []string{"{}", "null"} {
		installFakeDocker(t, "echo '"+empty+"'")
		if ids, orphans, err := cli.NetworkContainers(context.Background(), missingTestNetwork); err != nil || len(ids) != 0 || len(orphans) != 0 {
			t.Fatalf("%s: containers = %v orphans = %v err = %v", empty, ids, orphans, err)
		}
	}
	for _, invalid := range []string{"ep-", "ep-not-hex", "ep-0123456789abcdef/x", "xep-0123456789abcdef", "not-an-id"} {
		installFakeDocker(t, "echo '"+networkContainersJSON([2]string{missingTestContainer, "job"}, [2]string{invalid, "stale"})+"'")
		if ids, orphans, err := cli.NetworkContainers(context.Background(), missingTestNetwork); err == nil {
			t.Fatalf("invalid key %q accepted: %v %v", invalid, ids, orphans)
		}
	}
	installFakeDocker(t, "echo '"+networkContainersJSON(
		[2]string{"ep-89abcdef0123", "stale-endpoint"},
		[2]string{orphan, "stale-endpoint"},
	)+"'")
	if _, orphans, err := cli.NetworkContainers(context.Background(), missingTestNetwork); err != nil || !slices.Equal(orphans, []string{"stale-endpoint", "stale-endpoint"}) {
		t.Fatalf("orphans sharing a name = %v %v", orphans, err)
	}
	installFakeDocker(t, "echo 'not json'")
	if ids, orphans, err := cli.NetworkContainers(context.Background(), missingTestNetwork); err == nil {
		t.Fatalf("invalid inspect output accepted: %v %v", ids, orphans)
	}
}

func TestDockerCLIRefusesInvalidOrphanedEndpointNames(t *testing.T) {
	cli := DockerCLI{TempDir: privateDir(t)}
	for _, invalid := range []string{"", "-f", "--force", "-", ".hidden", "stale endpoint", "stale/endpoint", strings.Repeat("a", 129)} {
		installFakeDocker(t, "echo '"+networkContainersJSON([2]string{"ep-" + strings.Repeat("ab12", 16), invalid})+"'")
		if ids, orphans, err := cli.NetworkContainers(context.Background(), missingTestNetwork); err == nil {
			t.Fatalf("invalid orphan name %q accepted: %v %v", invalid, ids, orphans)
		}
		log := installFakeDocker(t, "exit 0")
		if err := cli.DisconnectNetwork(context.Background(), missingTestNetwork, invalid, true); err == nil {
			t.Fatalf("disconnect of %q accepted", invalid)
		}
		if err := cli.DisconnectOrphanEndpoint(context.Background(), missingTestNetwork, invalid); err == nil {
			t.Fatalf("orphan disconnect of %q accepted", invalid)
		}
		if raw, err := os.ReadFile(log); err == nil && len(raw) != 0 {
			t.Fatalf("invalid endpoint %q reached docker: %q", invalid, raw)
		}
	}
}

func TestDockerCLIMissingOutputIsNarrowPerCallSite(t *testing.T) {
	ctx := context.Background()
	calls := map[string]func(cli DockerCLI) error{
		"remove network": func(cli DockerCLI) error { return cli.RemoveNetwork(ctx, missingTestNetwork) },
		"network containers": func(cli DockerCLI) error {
			ids, orphans, err := cli.NetworkContainers(ctx, missingTestNetwork)
			if err == nil && (len(ids) != 0 || len(orphans) != 0) {
				t.Fatalf("missing network listed containers %v orphans %v", ids, orphans)
			}
			return err
		},
		"disconnect network": func(cli DockerCLI) error {
			return cli.DisconnectNetwork(ctx, missingTestNetwork, missingTestContainer, true)
		},
		"disconnect orphan": func(cli DockerCLI) error {
			return cli.DisconnectOrphanEndpoint(ctx, missingTestNetwork, missingTestOrphan)
		},
		"running": func(cli DockerCLI) error {
			running, err := cli.Running(ctx, missingTestContainer)
			if err == nil && running {
				t.Fatal("missing container reported running")
			}
			return err
		},
		"image present": func(cli DockerCLI) error {
			present, err := cli.ImagePresent(ctx, missingTestImage)
			if err == nil && present {
				t.Fatal("missing image reported present")
			}
			return err
		},
		"remove containers": func(cli DockerCLI) error {
			return cli.Remove(ctx, Objects{Containers: []string{missingTestContainer}})
		},
		"remove networks": func(cli DockerCLI) error {
			return cli.Remove(ctx, Objects{Networks: []string{missingTestNetwork}})
		},
		"remove volumes": func(cli DockerCLI) error {
			return cli.Remove(ctx, Objects{Volumes: []string{missingTestNetwork}})
		},
	}
	networkGone := "Error response from daemon: network " + missingTestNetwork + " not found"
	legacyNetworkGone := "Error: No such network: " + missingTestNetwork
	containerGone := "Error response from daemon: No such container: " + missingTestContainer
	inProgress := "Error response from daemon: removal of container " + missingTestContainer + " is already in progress"
	cases := []struct {
		call   string
		output string
		soft   bool
	}{
		{"remove network", networkGone, true},
		{"remove network", legacyNetworkGone, true},
		{"remove network", badDockerContext, false},
		{"remove network", "Error response from daemon: network our-preview-other not found", false},
		{"remove network", containerGone, false},
		{"remove network", networkGone + "\n" + badDockerContext, false},
		{"network containers", networkGone, true},
		{"network containers", legacyNetworkGone, true},
		{"network containers", badDockerContext, false},
		{"network containers", containerGone, false},
		{"disconnect network", networkGone, true},
		{"disconnect network", containerGone, true},
		{"disconnect network", "Error response from daemon: endpoint " + missingTestContainer + " not found", true},
		{"disconnect network", "Error response from daemon: container " + missingTestContainer + " is not connected to network " + missingTestNetwork, true},
		{"disconnect network", badDockerContext, false},
		{"disconnect orphan", networkGone, true},
		{"disconnect orphan", "Error response from daemon: endpoint " + missingTestOrphan + " not found", true},
		{"disconnect orphan", "Error response from daemon: container " + missingTestOrphan + " is not connected to network " + missingTestNetwork, false},
		{"disconnect orphan", "Error response from daemon: endpoint our-preview-other-gateway not found", false},
		{"disconnect orphan", "Error response from daemon: network " + missingTestNetwork + " has active endpoints", false},
		{"disconnect orphan", badDockerContext, false},
		{"disconnect network", "Error response from daemon: No such image: " + missingTestImage, false},
		{"running", containerGone, true},
		{"running", badDockerContext, false},
		{"running", networkGone, false},
		{"image present", "Error response from daemon: No such image: " + missingTestImage, true},
		{"image present", badDockerContext, false},
		{"image present", containerGone, false},
		{"remove containers", containerGone, true},
		{"remove containers", inProgress, true},
		{"remove containers", badDockerContext, false},
		{"remove containers", networkGone, false},
		{"remove containers", missingTestContainer + "\n" + badDockerContext, false},
		{"remove containers", missingTestContainer, false},
		{"remove networks", networkGone, true},
		{"remove networks", inProgress, false},
		{"remove networks", badDockerContext, false},
		{"remove volumes", "Error response from daemon: get " + missingTestNetwork + ": no such volume", true},
		{"remove volumes", badDockerContext, false},
	}
	for _, test := range cases {
		t.Run(test.call+": "+test.output, func(t *testing.T) {
			fakeDockerFailure(t, test.output)
			err := calls[test.call](DockerCLI{TempDir: privateDir(t)})
			if test.soft && err != nil {
				t.Fatalf("%s treated %q as a failure: %v", test.call, test.output, err)
			}
			if !test.soft && err == nil {
				t.Fatalf("%s treated %q as success", test.call, test.output)
			}
		})
	}
}
