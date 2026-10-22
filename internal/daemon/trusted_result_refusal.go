package daemon

import (
	"context"
	"errors"
	"strings"
)

const (
	invalidPathPrefix        = "error: invalid path '"
	trustedResultPlaceholder = "<runner result directory>"
	maxStagingRefusalMessage = 300
)

var errAgentResultResolveTimeout = errors.New("agent result resolution timed out")

func (t *trustedResultRepository) resolutionContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeoutCause(ctx, t.resolveTimeout, errAgentResultResolveTimeout)
}

func (t *trustedResultRepository) stepRefusal(ctx context.Context, repositoryID, label string, err error) error {
	if err == nil {
		return nil
	}
	var resultErr *agentResultError
	if errors.As(err, &resultErr) {
		return err
	}
	if errors.Is(context.Cause(ctx), errAgentResultResolveTimeout) {
		return agentResultRefusal(repositoryID, "%s took longer than %s to snapshot; keep large generated files out of the working tree or ignore them", label, t.resolveTimeout)
	}
	if ctx.Err() != nil {
		return err
	}
	var gitErr *trustedGitFailure
	if !errors.As(err, &gitErr) {
		return agentResultRefusal(repositoryID, "%s could not be copied by the runner (%s)", label, t.scrubbed(err.Error()))
	}
	if invalid, ok := invalidStagingPath(gitErr.stderr); ok {
		return agentResultRefusal(repositoryID, "%s has a path Git refuses to store: %s; rename or remove it", label, quotedEntry(invalid))
	}
	reason := gitErr.stderr
	if reason == "" {
		reason = gitErr.err.Error()
	}
	return agentResultRefusal(repositoryID, "%s could not be staged (git %s: %s)", label, gitErr.subcommand, t.scrubbed(reason))
}

func (t *trustedResultRepository) scrubbed(message string) string {
	if t.root != "" {
		message = strings.ReplaceAll(message, t.root, trustedResultPlaceholder)
	}
	message = strings.Join(strings.Fields(message), " ")
	if len(message) > maxStagingRefusalMessage {
		message = strings.ToValidUTF8(message[:maxStagingRefusalMessage], "") + "..."
	}
	return message
}

func invalidStagingPath(stderr string) (string, bool) {
	for _, line := range strings.Split(stderr, "\n") {
		rest, found := strings.CutPrefix(line, invalidPathPrefix)
		if !found {
			continue
		}
		invalid, terminated := strings.CutSuffix(rest, "'")
		if terminated && invalid != "" {
			return invalid, true
		}
	}
	return "", false
}
