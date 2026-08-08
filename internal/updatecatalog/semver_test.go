package updatecatalog

import "testing"

func TestSemanticVersionComparison(t *testing.T) {
	cases := []struct {
		left, right string
		want        int
	}{
		{"1.0.0", "1.0.0", 0},
		{"2.0.0", "1.9.9", 1},
		{"1.2.0", "1.3.0", -1},
		{"1.2.4", "1.2.3", 1},
		{"1.0.0", "1.0.0-rc.1", 1},
		{"1.0.0-alpha", "1.0.0-alpha.1", -1},
		{"1.0.0-alpha.1", "1.0.0-alpha.beta", -1},
		{"1.0.0-beta.2", "1.0.0-beta.11", -1},
		{"1.0.0-999999999999999999999999999999", "1.0.0-1000000000000000000000000000000", -1},
		{"1.0.0-rc.1", "1.0.0-rc.1", 0},
		{"1.0.0+one", "1.0.0+two", 0},
	}
	for _, test := range cases {
		left, err := parseSemanticVersion(test.left)
		if err != nil {
			t.Fatal(err)
		}
		right, err := parseSemanticVersion(test.right)
		if err != nil {
			t.Fatal(err)
		}
		if got := compareVersions(left, right); got != test.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", test.left, test.right, got, test.want)
		}
	}
}

func TestSemanticVersionRejectsInvalidForms(t *testing.T) {
	for _, value := range []string{"", "v1.2.3", "1.2", "01.2.3", "1.2.3-01", "1.2.3-", "1.2.3+"} {
		if _, err := parseSemanticVersion(value); err == nil {
			t.Errorf("accepted invalid version %q", value)
		}
	}
}
