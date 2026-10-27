package remoteconfig

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func restrictedConfig() map[string]any {
	config := dockerConfig()
	config["job_network"] = "restricted"
	config["job_network_name"] = "our-runner-jobs-dev"
	config["egress_policy_id"] = "local-dev-egress-not-enforced"
	return config
}

func TestParseDeliveryAcceptsRestrictedJobNetwork(t *testing.T) {
	delivery, err := ParseDelivery(encode(t, deliveryBody(restrictedConfig())), testRunnerID)
	if err != nil {
		t.Fatal(err)
	}
	remote := delivery.Remote
	if remote.JobNetwork != "restricted" || remote.JobNetworkName != "our-runner-jobs-dev" || remote.EgressPolicyID != "local-dev-egress-not-enforced" {
		t.Fatalf("remote = %+v", remote)
	}
	for _, value := range []string{"a", strings.Repeat("a", 128), "Net_1.x-y", "9net"} {
		config := restrictedConfig()
		config["job_network_name"] = value
		config["egress_policy_id"] = value
		if _, err := ParseDelivery(encode(t, deliveryBody(config)), testRunnerID); err != nil {
			t.Fatalf("%q rejected: %v", value, err)
		}
	}
	for _, reserved := range []string{"host", "bridge", "none", "default"} {
		config := restrictedConfig()
		config["egress_policy_id"] = reserved
		if _, err := ParseDelivery(encode(t, deliveryBody(config)), testRunnerID); err != nil {
			t.Fatalf("reserved network name %q rejected as a policy ID: %v", reserved, err)
		}
	}
}

func TestParseDeliveryKeepsTheNoneDocumentUnchanged(t *testing.T) {
	delivery, err := ParseDelivery(encode(t, deliveryBody(dockerConfig())), testRunnerID)
	if err != nil {
		t.Fatal(err)
	}
	if delivery.Remote.JobNetwork != "none" || delivery.Remote.JobNetworkName != "" || delivery.Remote.EgressPolicyID != "" {
		t.Fatalf("remote = %+v", delivery.Remote)
	}
	withoutNetwork := dockerConfig()
	delete(withoutNetwork, "job_network")
	if _, err := ParseDelivery(encode(t, deliveryBody(withoutNetwork)), testRunnerID); err != nil {
		t.Fatalf("docker document without job_network rejected: %v", err)
	}
}

func TestParseDeliveryRejectsInvalidRestrictedJobNetworks(t *testing.T) {
	cases := map[string]struct {
		mutate func(map[string]any)
		key    string
	}{
		"name with none":            {func(c map[string]any) { c["job_network"] = "none"; delete(c, "egress_policy_id") }, "job_network_name"},
		"policy with none":          {func(c map[string]any) { c["job_network"] = "none"; delete(c, "job_network_name") }, "egress_policy_id"},
		"name without job_network":  {func(c map[string]any) { delete(c, "job_network"); delete(c, "egress_policy_id") }, "job_network_name"},
		"restricted missing name":   {func(c map[string]any) { delete(c, "job_network_name") }, "job_network_name"},
		"restricted missing policy": {func(c map[string]any) { delete(c, "egress_policy_id") }, "egress_policy_id"},
		"name number":               {func(c map[string]any) { c["job_network_name"] = 1 }, "job_network_name"},
		"name null":                 {func(c map[string]any) { c["job_network_name"] = nil }, "job_network_name"},
		"policy null":               {func(c map[string]any) { c["egress_policy_id"] = nil }, "egress_policy_id"},
		"name empty":                {func(c map[string]any) { c["job_network_name"] = "" }, "job_network_name"},
		"policy empty":              {func(c map[string]any) { c["egress_policy_id"] = "" }, "egress_policy_id"},
		"name 129":                  {func(c map[string]any) { c["job_network_name"] = strings.Repeat("a", 129) }, "job_network_name"},
		"policy 129":                {func(c map[string]any) { c["egress_policy_id"] = strings.Repeat("a", 129) }, "egress_policy_id"},
		"name leading dot":          {func(c map[string]any) { c["job_network_name"] = ".net" }, "job_network_name"},
		"policy leading dash":       {func(c map[string]any) { c["egress_policy_id"] = "-policy" }, "egress_policy_id"},
		"name space":                {func(c map[string]any) { c["job_network_name"] = "our net" }, "job_network_name"},
		"name surrounding space":    {func(c map[string]any) { c["job_network_name"] = " net" }, "job_network_name"},
		"name slash":                {func(c map[string]any) { c["job_network_name"] = "a/b" }, "job_network_name"},
		"policy control":            {func(c map[string]any) { c["egress_policy_id"] = "policy\n" }, "egress_policy_id"},
		"name host":                 {func(c map[string]any) { c["job_network_name"] = "host" }, "job_network_name"},
		"name bridge":               {func(c map[string]any) { c["job_network_name"] = "bridge" }, "job_network_name"},
		"name none":                 {func(c map[string]any) { c["job_network_name"] = "none" }, "job_network_name"},
		"name default":              {func(c map[string]any) { c["job_network_name"] = "default" }, "job_network_name"},
		"unknown mode":              {func(c map[string]any) { c["job_network"] = "open" }, "job_network"},
		"mode number":               {func(c map[string]any) { c["job_network"] = 1 }, "job_network"},
	}
	for name, test := range cases {
		config := restrictedConfig()
		test.mutate(config)
		_, err := ParseDelivery(encode(t, deliveryBody(config)), testRunnerID)
		if err == nil || !strings.Contains(err.Error(), test.key) {
			t.Fatalf("%s: err = %v, want it to name %s", name, err, test.key)
		}
	}
	plainNone := dockerConfig()
	plainNone["job_network"] = "open"
	if _, err := ParseDelivery(encode(t, deliveryBody(plainNone)), testRunnerID); err == nil {
		t.Fatal("accepted an unknown job_network on a docker document")
	}
}

func TestParseDeliveryRejectsNetworkKeysUnderDirectIsolation(t *testing.T) {
	for name, keys := range map[string]map[string]any{
		"restricted triple": {"job_network": "restricted", "job_network_name": "our-runner-jobs-dev", "egress_policy_id": "policy"},
		"restricted only":   {"job_network": "restricted"},
		"none":              {"job_network": "none"},
		"name only":         {"job_network_name": "our-runner-jobs-dev"},
		"policy only":       {"egress_policy_id": "policy"},
	} {
		config := directConfig()
		for key, value := range keys {
			config[key] = value
		}
		_, err := ParseDelivery(encode(t, deliveryBody(config)), testRunnerID)
		if err == nil || !strings.Contains(err.Error(), "direct") {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
}

func TestParseDeliveryRejectsDuplicateNetworkKeys(t *testing.T) {
	raw := string(encode(t, deliveryBody(restrictedConfig())))
	duplicated := strings.Replace(raw, `"job_network_name":"our-runner-jobs-dev"`, `"job_network_name":"our-runner-jobs-dev","job_network_name":"other"`, 1)
	if duplicated == raw {
		t.Fatal("fixture did not contain the network name")
	}
	if _, err := ParseDelivery([]byte(duplicated), testRunnerID); err == nil || !strings.Contains(err.Error(), "job_network_name") {
		t.Fatalf("err = %v", err)
	}
}

func TestMergeCopiesTheRestrictedJobNetwork(t *testing.T) {
	merged, err := Merge(testLocal(t), parsed(t, deliveryBody(restrictedConfig())))
	if err != nil {
		t.Fatal(err)
	}
	cfg := merged.Config
	if cfg.JobNetwork != "restricted" || cfg.JobNetworkName != "our-runner-jobs-dev" || cfg.EgressPolicyID != "local-dev-egress-not-enforced" {
		t.Fatalf("config = %+v", cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	none, err := Merge(testLocal(t), parsed(t, deliveryBody(dockerConfig())))
	if err != nil {
		t.Fatal(err)
	}
	if none.Config.JobNetwork != "none" || none.Config.JobNetworkName != "" || none.Config.EgressPolicyID != "" {
		t.Fatalf("none config = %+v", none.Config)
	}
}

func TestRestartRequiredChangesComparesTheNetworkIdentity(t *testing.T) {
	running := parsed(t, deliveryBody(restrictedConfig())).Remote
	for key, value := range map[string]string{"job_network_name": "our-runner-jobs-other", "egress_policy_id": "other-policy"} {
		config := restrictedConfig()
		config[key] = value
		next := parsed(t, deliveryBody(config)).Remote
		keys := RestartRequiredChanges(running, next)
		if len(keys) != 1 || keys[0] != key {
			t.Fatalf("%s: keys = %v", key, keys)
		}
	}
	hot := restrictedConfig()
	hot["labels"] = map[string]any{"pool": "other"}
	hot["repositories"] = map[string]any{}
	if keys := RestartRequiredChanges(running, parsed(t, deliveryBody(hot)).Remote); len(keys) != 0 {
		t.Fatalf("hot change keys = %v", keys)
	}
	none := parsed(t, deliveryBody(dockerConfig())).Remote
	keys := RestartRequiredChanges(none, running)
	if strings.Join(keys, ",") != "job_network,job_network_name,egress_policy_id" {
		t.Fatalf("none to restricted keys = %v", keys)
	}
}

func TestReloaderRefusesANetworkIdentityChangeAndKeepsTheCache(t *testing.T) {
	for key, value := range map[string]string{"job_network_name": "our-runner-jobs-other", "egress_policy_id": "other-policy"} {
		cached := encode(t, deliveryBody(restrictedConfig()))
		local := configuredState(t, cached)
		next := restrictedConfig()
		next[key] = value
		script := &conditionalScript{responses: []conditionalResponse{{raw: encode(t, bodyWithDigest(next, strings.Repeat("f", 64)))}}}
		reloader := runningReloader(t, local, deliveryBody(restrictedConfig()), script.fetch)
		reload, err := reloader.Check(context.Background())
		if err != nil || reload.Outcome != ReloadRestartRequired || !reflect.DeepEqual(reload.RestartKeys, []string{key}) {
			t.Fatalf("%s: reload = %+v err = %v", key, reload, err)
		}
		if reloader.RunningDigest() != strings.Repeat("d", 64) {
			t.Fatalf("%s: running digest moved", key)
		}
		stored, _ := LoadCachedDelivery(local.StateDir)
		if string(stored) != string(cached) {
			t.Fatalf("%s: a restart-required document replaced the cache", key)
		}
	}
}

func TestReloaderAppliesAHotChangeWithAnUnchangedRestrictedNetwork(t *testing.T) {
	local := configuredState(t, encode(t, deliveryBody(restrictedConfig())))
	next := restrictedConfig()
	next["labels"] = map[string]any{"pool": "us-linux"}
	next["repositories"] = map[string]any{"repo-9": map[string]any{"url": "https://github.com/acme/new.git"}}
	script := &conditionalScript{responses: []conditionalResponse{{raw: encode(t, bodyWithDigest(next, strings.Repeat("e", 64)))}}}
	reload, err := runningReloader(t, local, deliveryBody(restrictedConfig()), script.fetch).Check(context.Background())
	if err != nil || reload.Outcome != ReloadApply {
		t.Fatalf("reload = %+v err = %v", reload, err)
	}
	cfg := reload.Merged.Config
	if cfg.Labels["pool"] != "us-linux" || cfg.JobNetworkName != "our-runner-jobs-dev" || cfg.EgressPolicyID != "local-dev-egress-not-enforced" {
		t.Fatalf("config = %+v", cfg)
	}
}
