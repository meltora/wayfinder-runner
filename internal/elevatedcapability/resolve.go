package elevatedcapability

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"sort"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/safeio"
)

const MaxDeclarationBytes = 64 << 10

type Reason string

const (
	ReasonApplied               Reason = "applied"
	ReasonExpired               Reason = "expired"
	ReasonAdapterUnsupported    Reason = "adapter_unsupported"
	ReasonReadOnlyJob           Reason = "read_only_job"
	ReasonDeclarationUnreadable Reason = "declaration_unreadable"
	ReasonDigestMismatch        Reason = "digest_mismatch"
	ReasonDeclarationInvalid    Reason = "declaration_invalid"
	ReasonServerNameConflict    Reason = "server_name_conflict"
	ReasonHostInvalid           Reason = "host_invalid"
	ReasonHookScriptOnly        Reason = "hook_script_not_a_declaration"
	ReasonRemoteHostNotApproved Reason = "remote_host_not_approved"

	ReasonCommandReferencesRepositoryFile Reason = "command_references_repository_file"
	ReasonReferencedFileUnverifiable      Reason = "referenced_file_unverifiable"
	ReasonProgramNotAbsolute              Reason = "program_not_absolute"
	ReasonHookEventNotAllowed             Reason = "hook_event_not_allowed"
	ReasonRepositoryNotBound              Reason = "repository_not_bound"
	ReasonRepositoryNotInJob              Reason = "repository_not_in_job"
	ReasonContainerProgramUnverifiable    Reason = "container_program_unverifiable"
	ReasonAgentPackNotInJob               Reason = "agent_pack_not_in_job"
)

var Reasons = []Reason{
	ReasonApplied, ReasonExpired, ReasonAdapterUnsupported, ReasonReadOnlyJob, ReasonDeclarationUnreadable,
	ReasonDigestMismatch, ReasonDeclarationInvalid, ReasonServerNameConflict, ReasonHostInvalid, ReasonHookScriptOnly,
	ReasonRemoteHostNotApproved, ReasonCommandReferencesRepositoryFile, ReasonReferencedFileUnverifiable,
	ReasonProgramNotAbsolute, ReasonHookEventNotAllowed, ReasonRepositoryNotBound, ReasonRepositoryNotInJob,
	ReasonContainerProgramUnverifiable, ReasonAgentPackNotInJob,
}

type Outcome struct {
	RequestID string
	Kind      v1.ElevatedCapabilityKind
	Key       string
	Digest    string
	Reason    Reason
}

func (outcome Outcome) Applied() bool { return outcome.Reason == ReasonApplied }

type Grants struct {
	MCPServers     map[string]map[string]any
	Hooks          map[string][]map[string]any
	AllowedDomains []string
}

func (grants Grants) Empty() bool {
	return len(grants.MCPServers) == 0 && len(grants.Hooks) == 0 && len(grants.AllowedDomains) == 0
}

func (grants Grants) MCPServerNames() []string {
	names := make([]string, 0, len(grants.MCPServers))
	for name := range grants.MCPServers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

type Input struct {
	Grants             *v1.ElevatedCapabilityGrants
	Repositories       map[string]string
	AgentPackDigest    string
	AgentPackDir       string
	ProtectedDirs      []string
	Adapter            string
	ReadOnly           bool
	ContainerIsolation bool
	Now                time.Time
}

type Resolution struct {
	Grants   Grants
	Outcomes []Outcome
}

func Resolve(input Input) Resolution {
	resolution := Resolution{Grants: Grants{MCPServers: map[string]map[string]any{}, Hooks: map[string][]map[string]any{}}}
	if input.Grants == nil {
		return resolution
	}
	grants := append([]v1.ElevatedCapabilityGrant(nil), input.Grants.Grants...)
	sort.SliceStable(grants, func(left, right int) bool {
		if grants[left].Kind != grants[right].Kind {
			return grants[left].Kind < grants[right].Kind
		}
		if grants[left].Key != grants[right].Key {
			return grants[left].Key < grants[right].Key
		}
		return grants[left].RequestID < grants[right].RequestID
	})
	domains := map[string]bool{}
	reasons := map[string]Reason{}
	protectedDirs := append([]string(nil), input.ProtectedDirs...)
	for _, checkout := range input.Repositories {
		protectedDirs = append(protectedDirs, checkout)
	}
	if input.AgentPackDir != "" {
		protectedDirs = append(protectedDirs, input.AgentPackDir)
	}
	sort.Strings(protectedDirs)
	for _, grant := range grants {
		if grant.Kind != v1.ElevatedCapabilityNetworkAccess {
			continue
		}
		reason := eligibility(input, grant)
		if reason == "" {
			reason = ReasonHostInvalid
			if host, ok := NormalizeHost(grant.Key); ok {
				domains[host] = true
				reason = ReasonApplied
			}
		}
		reasons[grant.RequestID] = reason
	}
	for _, grant := range grants {
		if grant.Kind == v1.ElevatedCapabilityNetworkAccess {
			continue
		}
		reason := eligibility(input, grant)
		if reason == "" {
			reason = declarationSourceReason(input, grant)
		}
		if reason == "" {
			scope := newCommandScope(declarationSourceDir(input, grant), protectedDirs)
			scope.container = input.ContainerIsolation
			reason = resolution.applyDeclaration(scope, grant, domains)
		}
		reasons[grant.RequestID] = reason
	}
	for _, grant := range grants {
		outcome := Outcome{RequestID: grant.RequestID, Kind: grant.Kind, Key: grant.Key, Digest: grant.DeclarationDigest, Reason: reasons[grant.RequestID]}
		if !GrantShapeValid(grant) {
			outcome = Outcome{RequestID: grant.RequestID, Reason: ReasonDeclarationInvalid}
		}
		resolution.Outcomes = append(resolution.Outcomes, outcome)
	}
	for domain := range domains {
		resolution.Grants.AllowedDomains = append(resolution.Grants.AllowedDomains, domain)
	}
	sort.Strings(resolution.Grants.AllowedDomains)
	return resolution
}

func eligibility(input Input, grant v1.ElevatedCapabilityGrant) Reason {
	switch {
	case !GrantShapeValid(grant):
		return ReasonDeclarationInvalid
	case !grant.ExpiresAt.After(input.Now):
		return ReasonExpired
	case input.Adapter != "claude":
		return ReasonAdapterUnsupported
	case input.ReadOnly:
		return ReasonReadOnlyJob
	}
	return ""
}

func declarationSourceReason(input Input, grant v1.ElevatedCapabilityGrant) Reason {
	if grant.AgentPackDigest != "" {
		if input.AgentPackDir == "" || input.AgentPackDigest != grant.AgentPackDigest {
			return ReasonAgentPackNotInJob
		}
		return ""
	}
	if grant.RepositoryID == "" {
		return ReasonRepositoryNotBound
	}
	if input.Repositories[grant.RepositoryID] == "" {
		return ReasonRepositoryNotInJob
	}
	return ""
}

func declarationSourceDir(input Input, grant v1.ElevatedCapabilityGrant) string {
	if grant.AgentPackDigest != "" {
		return input.AgentPackDir
	}
	return input.Repositories[grant.RepositoryID]
}

func (resolution *Resolution) applyDeclaration(scope commandScope, grant v1.ElevatedCapabilityGrant, domains map[string]bool) Reason {
	content, reason := readDeclaration(scope.workDir, grant)
	if reason != "" {
		return reason
	}
	switch grant.Kind {
	case v1.ElevatedCapabilityMCPServer:
		return resolution.applyMCP(scope, content, domains)
	case v1.ElevatedCapabilityHook:
		return resolution.applyHook(scope, grant.Key, content)
	default:
		return ReasonDeclarationInvalid
	}
}

func (resolution *Resolution) applyMCP(scope commandScope, content []byte, domains map[string]bool) Reason {
	servers, err := ParseMCPDeclaration(content)
	if err != nil {
		return ReasonDeclarationInvalid
	}
	for _, name := range sortedServerNames(servers) {
		server := servers[name]
		if _, taken := resolution.Grants.MCPServers[name]; taken {
			return ReasonServerNameConflict
		}
		if remote, ok := server["url"].(string); ok && !HostAllowed(remote, domains) {
			return ReasonRemoteHostNotApproved
		}
		if _, stdio := server["command"]; stdio && scope.container {
			return ReasonContainerProgramUnverifiable
		}
		if reason := scope.mcpCommandReason(server); reason != "" {
			return reason
		}
	}
	for name, server := range servers {
		resolution.Grants.MCPServers[name] = server
	}
	return ReasonApplied
}

func sortedServerNames(servers map[string]map[string]any) []string {
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (resolution *Resolution) applyHook(scope commandScope, key string, content []byte) Reason {
	if !IsHookDeclarationPath(key) {
		return ReasonHookScriptOnly
	}
	hooks, err := ParseHookDeclaration(content)
	if err != nil {
		return ReasonDeclarationInvalid
	}
	for event := range hooks {
		if !grantableHookEvents[event] {
			return ReasonHookEventNotAllowed
		}
	}
	if scope.container {
		return ReasonContainerProgramUnverifiable
	}
	if reason := scope.hookCommandsReason(hooks); reason != "" {
		return reason
	}
	for event, groups := range hooks {
		resolution.Grants.Hooks[event] = append(resolution.Grants.Hooks[event], groups...)
	}
	return ReasonApplied
}

func readDeclaration(workDir string, grant v1.ElevatedCapabilityGrant) ([]byte, Reason) {
	if workDir == "" {
		return nil, ReasonDeclarationUnreadable
	}
	file, err := safeio.OpenRegular(workDir, grant.Key)
	if err != nil {
		return nil, ReasonDeclarationUnreadable
	}
	defer func() { _ = file.Close() }()
	content, err := io.ReadAll(io.LimitReader(file, MaxDeclarationBytes+1))
	if err != nil || len(content) > MaxDeclarationBytes {
		return nil, ReasonDeclarationUnreadable
	}
	sum := sha256.Sum256(content)
	if hex.EncodeToString(sum[:]) != grant.DeclarationDigest {
		return nil, ReasonDigestMismatch
	}
	return content, ""
}
