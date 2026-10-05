package capability

import "strings"

// WithProviderBinaries copies operator configuration without exposing private
// executable paths in the capability manifest. Config validation owns the
// absolute-path requirement; an empty override retains the adapter default.
func (d *Detector) WithProviderBinaries(binaries map[string]string) *Detector {
	configured := *d
	configured.providerBinaries = make(map[string]string, 3)
	for _, name := range []string{"codex", "claude", "opencode"} {
		configured.providerBinaries[name] = binaries[name]
	}
	return &configured
}

func (d *Detector) providerBinary(name string) string {
	switch name {
	case "codex", "claude", "opencode":
		if binary := d.providerBinaries[name]; binary != "" {
			return binary
		}
		return name
	default:
		return ""
	}
}

// providerVersion omits output that echoes a known private executable path.
// Ordinary provider names remain valid version text; this is not a general
// sanitizer for arbitrary output emitted by operator-configured executables.
func providerVersion(name, output string, paths ...string) string {
	for _, path := range paths {
		if path != "" && path != name && strings.Contains(output, path) {
			return ""
		}
	}
	return output
}
