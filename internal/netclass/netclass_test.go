package netclass

import (
	"net/netip"
	"testing"
)

func TestPublicRefusesEveryDeniedClass(t *testing.T) {
	for address, want := range map[string]bool{
		"93.184.216.34":          true,
		"1.1.1.1":                true,
		"2606:4700::1111":        true,
		"2a00:1450::200e":        true,
		"10.1.2.3":               false,
		"172.16.0.1":             false,
		"192.168.1.1":            false,
		"127.0.0.1":              false,
		"169.254.169.254":        false,
		"100.64.0.1":             false,
		"0.0.0.0":                false,
		"0.1.2.3":                false,
		"192.0.0.8":              false,
		"192.0.2.1":              false,
		"198.18.0.1":             false,
		"198.19.255.255":         false,
		"198.51.100.1":           false,
		"203.0.113.1":            false,
		"240.0.0.1":              false,
		"255.255.255.255":        false,
		"224.0.0.1":              false,
		"::":                     false,
		"::1":                    false,
		"fe80::1":                false,
		"fd00::1":                false,
		"fd00:ec2::254":          false,
		"ff02::1":                false,
		"64:ff9b::a00:1":         false,
		"64:ff9b:1::1":           false,
		"100::1":                 false,
		"2001::1":                false,
		"2001:db8::1":            false,
		"2002::1":                false,
		"::ffff:10.0.0.1":        false,
		"::ffff:169.254.169.254": false,
		"::ffff:93.184.216.34":   true,
	} {
		if got := Public(netip.MustParseAddr(address)); got != want {
			t.Fatalf("Public(%s) = %v, want %v", address, got, want)
		}
	}
	if Public(netip.Addr{}) {
		t.Fatal("invalid address is public")
	}
	if Public(netip.MustParseAddr("2606:4700::1111%eth0")) {
		t.Fatal("zoned address is public")
	}
}

func TestEveryDeniedSpecialRangeIsRefused(t *testing.T) {
	for _, prefix := range DeniedSpecialRanges {
		if !InDeniedSpecialRange(prefix.Addr()) || Public(prefix.Addr()) {
			t.Fatalf("range %s is not refused", prefix)
		}
	}
	if InDeniedSpecialRange(netip.MustParseAddr("93.184.216.34")) {
		t.Fatal("public address is in a denied range")
	}
}
