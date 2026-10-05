package validation

import "testing"

func TestSignedAgentPackAuthorityChecksBlankPackNamesWithTheECMAScriptWhitespaceSet(t *testing.T) {
	for _, tc := range []struct {
		name     string
		packName string
		valid    bool
	}{
		{name: "byte order mark only", packName: "\ufeff", valid: false},
		{name: "ideographic space and tab", packName: "\u3000\t", valid: false},
		{name: "line separator only", packName: "\u2028", valid: false},
		{name: "next line only", packName: "\u0085", valid: true},
		{name: "zero width space only", packName: "\u200b", valid: true},
		{name: "visible text inside whitespace", packName: "\ufeff Base pack \u3000", valid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, authority := validAgentPackAuthority()
			authority.PackName = tc.packName
			err := SignedAgentPackAuthority(*authority)
			if tc.valid && err != nil {
				t.Fatalf("pack name %+q refused: %v", tc.packName, err)
			}
			if !tc.valid && err == nil {
				t.Fatalf("pack name %+q accepted", tc.packName)
			}
		})
	}
}
