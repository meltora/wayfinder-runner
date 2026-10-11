package trustroot

import (
	"embed"
	"fmt"
	"io/fs"
	"sort"

	"github.com/example/app-runner/internal/safeio"
)

const (
	SourceEmbedded            = "embedded"
	SourceDevelopmentOverride = "development_override"
	OverrideEnvironment       = "OUR_RUNNER_DEVELOPMENT_TRUST_ROOTS_FILE"
)

//go:embed roots/*.json
var committedRoots embed.FS

var embeddedRoots fs.FS = committedRoots

type Roots struct {
	Channel string
	Source  string
	Keys    []Root
}

func (roots Roots) Find(keyID string) (Root, bool) {
	for _, root := range roots.Keys {
		if root.KeyID == keyID {
			return root, true
		}
	}
	return Root{}, false
}

func (roots Roots) KeyIDs() []string {
	ids := make([]string, 0, len(roots.Keys))
	for _, root := range roots.Keys {
		ids = append(ids, root.KeyID)
	}
	sort.Strings(ids)
	return ids
}

func Embedded(channel string) (Roots, error) {
	raw, err := fs.ReadFile(embeddedRoots, "roots/"+channel+".json")
	if err != nil {
		return Roots{}, fmt.Errorf("this build has no trust roots for the %s channel", channel)
	}
	keys, err := ParseRootsFile(raw)
	if err != nil {
		return Roots{}, fmt.Errorf("embedded %s trust roots: %w", channel, err)
	}
	return Roots{Channel: channel, Source: SourceEmbedded, Keys: keys}, nil
}

func Resolve(daemonChannel, overridePath string) (Roots, error) {
	channel, err := ReleaseChannel(daemonChannel)
	if err != nil {
		return Roots{}, err
	}
	if overridePath == "" {
		return Embedded(channel)
	}
	if channel != ChannelDevelopment {
		return Roots{}, fmt.Errorf("%s is refused on the %s channel; trust root overrides are accepted only by development builds", OverrideEnvironment, channel)
	}
	raw, err := safeio.ReadRegularFile(overridePath, MaxRootsFileBytes, 0o022)
	if err != nil {
		return Roots{}, fmt.Errorf("read development trust roots file: %w", err)
	}
	keys, err := ParseRootsFile(raw)
	if err != nil {
		return Roots{}, fmt.Errorf("development trust roots file: %w", err)
	}
	if len(keys) == 0 {
		return Roots{}, fmt.Errorf("development trust roots file must list at least one root")
	}
	return Roots{Channel: channel, Source: SourceDevelopmentOverride, Keys: keys}, nil
}
