package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/example/app-runner/internal/remoteconfig"
	"github.com/example/app-runner/internal/safeio"
	"gopkg.in/yaml.v3"
)

const ignoredStateDirConfigFileName = "config.yaml"

func warnAboutStateDirConfiguration(warnings io.Writer, stateDir string, local remoteconfig.Local, merged remoteconfig.Merged) {
	warnAboutIgnoredConfigFile(warnings, stateDir, local)
	for _, omitted := range merged.OmittedChecks {
		fmt.Fprintf(warnings, "warning: local check %s is not registered: its repository_id %s is not in the delivered runner configuration\n",
			omitted.ID, omitted.RepositoryID)
	}
}

func warnAboutIgnoredConfigFile(warnings io.Writer, stateDir string, local remoteconfig.Local) {
	path := filepath.Join(stateDir, ignoredStateDirConfigFileName)
	raw, err := safeio.ReadRegularFile(path, 1<<20, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	message := fmt.Sprintf("warning: %s is not read with --state-dir; this runner uses the dashboard configuration and %s", path, remoteconfig.LocalFileName)
	var document struct {
		Checks map[string]yaml.Node `yaml:"checks"`
	}
	if err == nil && yaml.Unmarshal(raw, &document) == nil && len(document.Checks) > 0 && len(local.Checks) == 0 {
		ids := slices.Sorted(maps.Keys(document.Checks))
		message += fmt.Sprintf("; its checks (%s) are not registered: move them to %s", strings.Join(ids, ", "),
			filepath.Join(stateDir, remoteconfig.LocalFileName))
	}
	fmt.Fprintln(warnings, message)
}
