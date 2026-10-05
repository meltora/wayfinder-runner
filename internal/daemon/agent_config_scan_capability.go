package daemon

import (
	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/validation"
	"net/url"
	"sort"
)

// Only configured remote registrations are advertised. Local test repositories
// and unsafe direct execution settings cannot grant this transport authority.
func (d *Daemon) agentConfigScanRepositories() []string {
	return d.agentConfigScanRepositoriesFrom(d.hotConfiguration().repositories)
}

func (d *Daemon) agentConfigScanRepositoriesFrom(repositories map[string]config.RepositoryConfig) []string {
	if len(repositories) > 256 {
		return nil
	}
	ids := make([]string, 0, len(repositories))
	for id, repository := range repositories {
		parsed, err := url.Parse(repository.URL)
		if validation.ID("repository_id", id) != nil || err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "ssh") || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.ForceQuery {
			continue
		}
		if parsed.User != nil {
			if _, password := parsed.User.Password(); password {
				continue
			}
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
