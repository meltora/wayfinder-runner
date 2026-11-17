package refusedrequest

import (
	"github.com/example/app-runner/internal/agentactivity"

	v1 "github.com/example/app-runner/api/runner/v1"
)

type Admitted struct {
	Sequence  int
	Kind      v1.RefusalKind
	Enforcer  v1.RefusalEnforcer
	Sanitized agentactivity.Sanitized
}

type Recorder struct {
	maxEvents int
	issued    int
	seen      map[string]struct{}
}

func NewRecorder(maxEvents int) *Recorder {
	return &Recorder{maxEvents: maxEvents, seen: map[string]struct{}{}}
}

func (r *Recorder) Admit(item Item, knownSecrets ...string) (Admitted, bool) {
	return r.admit(v1.RefusalEnforcerAgentRuntimePermissions, item, knownSecrets)
}

func (r *Recorder) AdmitNetworkBlock(block NetworkBlock, knownSecrets ...string) (Admitted, bool) {
	return r.admit(block.Enforcer, Item{Kind: v1.RefusalNetworkDestination, Subject: block.Subject}, knownSecrets)
}

func (r *Recorder) admit(enforcer v1.RefusalEnforcer, item Item, knownSecrets []string) (Admitted, bool) {
	if r.issued >= r.maxEvents {
		return Admitted{}, false
	}
	sanitized := agentactivity.Sanitize(item.Subject, v1.MaxRefusedRequestSubjectBytes, knownSecrets...)
	if sanitized.Message == "" {
		return Admitted{}, false
	}
	key := string(enforcer) + "\x00" + string(item.Kind) + "\x00" + sanitized.Message
	if _, duplicate := r.seen[key]; duplicate {
		return Admitted{}, false
	}
	r.seen[key] = struct{}{}
	r.issued++
	return Admitted{Sequence: r.issued, Kind: item.Kind, Enforcer: enforcer, Sanitized: sanitized}, true
}
