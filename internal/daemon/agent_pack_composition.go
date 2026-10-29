package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/safeio"
	"github.com/example/app-runner/internal/scanner"
	"github.com/example/app-runner/internal/validation"
)

type compositionRepository struct {
	ID      string
	Root    string
	Primary bool
}

type compositionInput struct {
	Adapter       string
	Isolation     string
	Pack          *v1.AgentPackRef
	PackFiles     map[string]string
	PluginLoaded  bool
	ConfigDir     string
	HostHome      string
	HostClaudeDir string
	Repositories  []compositionRepository
}

type compositionBody struct {
	AgentPack *v1.AgentPackCompositionPack
	Adapter   string
	Isolation string
	Layers    []v1.AgentPackCompositionLayer
	Conflicts []v1.AgentPackCompositionConflict
	Truncated bool
}

func (body compositionBody) apply(event *v1.AgentPackCompositionEvent, phase, packView string) {
	event.SchemaVersion = v1.AgentPackCompositionSchemaVersion
	event.Phase = phase
	event.Adapter = body.Adapter
	event.Isolation = body.Isolation
	if body.AgentPack != nil {
		pack := *body.AgentPack
		event.AgentPack = &pack
	}
	event.PackView = packView
	event.Layers = make([]v1.AgentPackCompositionLayer, len(body.Layers))
	for index, layer := range body.Layers {
		layer.Sources = append([]v1.AgentPackCompositionSource{}, layer.Sources...)
		event.Layers[index] = layer
	}
	event.Conflicts = append([]v1.AgentPackCompositionConflict{}, body.Conflicts...)
	event.Truncated = body.Truncated
}

func agentPackCompositionEventID(attemptID, leaseID string, fencingToken uint64, phase string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d\x00%s", attemptID, leaseID, fencingToken, phase)))
	return "composition-" + hex.EncodeToString(sum[:])
}

type compositionBuilder struct {
	body      compositionBody
	layers    map[string]*v1.AgentPackCompositionLayer
	conflicts []v1.AgentPackCompositionConflict
}

func buildAgentPackComposition(input compositionInput) (compositionBody, error) {
	builder := &compositionBuilder{
		body:   compositionBody{Adapter: input.Adapter, Isolation: input.Isolation},
		layers: map[string]*v1.AgentPackCompositionLayer{},
	}
	for _, name := range v1.AgentPackCompositionLayerOrder {
		builder.layers[name] = &v1.AgentPackCompositionLayer{Layer: name, Status: v1.AgentPackCompositionAbsent, Sources: []v1.AgentPackCompositionSource{}}
	}
	if err := builder.platformSafety(input.ConfigDir); err != nil {
		return compositionBody{}, err
	}
	packSkills := builder.agentPack(input)
	for _, repository := range input.Repositories {
		inventory, err := scanner.Scan(repository.Root)
		if err != nil {
			return compositionBody{}, fmt.Errorf("composition scan %s: %w", repository.ID, err)
		}
		builder.repository(input.Adapter, repository, inventory, packSkills)
	}
	builder.layers[v1.AgentPackCompositionLayerExecutionManifest].Status = v1.AgentPackCompositionApplied
	builder.layers[v1.AgentPackCompositionLayerTicketContext].Status = v1.AgentPackCompositionApplied
	builder.hostUserConfiguration(input)

	for _, name := range v1.AgentPackCompositionLayerOrder {
		layer := builder.layers[name]
		if len(layer.Sources) > v1.MaxAgentPackCompositionSourcesPerLayer {
			layer.Sources = layer.Sources[:v1.MaxAgentPackCompositionSourcesPerLayer]
			builder.body.Truncated = true
		}
		builder.body.Layers = append(builder.body.Layers, *layer)
	}
	builder.body.Conflicts = builder.conflicts
	if builder.body.Conflicts == nil {
		builder.body.Conflicts = []v1.AgentPackCompositionConflict{}
	}
	if len(builder.body.Conflicts) > v1.MaxAgentPackCompositionConflicts {
		builder.body.Conflicts = builder.body.Conflicts[:v1.MaxAgentPackCompositionConflicts]
		builder.body.Truncated = true
	}
	return builder.body, nil
}

func (builder *compositionBuilder) platformSafety(configDir string) error {
	layer := builder.layers[v1.AgentPackCompositionLayerPlatformSafety]
	if configDir == "" {
		return nil
	}
	entries, err := os.ReadDir(configDir)
	if err != nil {
		return fmt.Errorf("composition platform configuration: %w", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == "job.token" || !entry.Type().IsRegular() {
			continue
		}
		digest, err := compositionFileDigest(configDir, name)
		if err != nil {
			return err
		}
		sourceType := v1.AgentConfigProviderConfiguration
		switch {
		case strings.HasPrefix(name, "managed-hook"):
			sourceType = v1.AgentConfigHook
		case strings.Contains(name, "mcp"):
			sourceType = v1.AgentConfigMCPDeclaration
		}
		layer.Sources = append(layer.Sources, v1.AgentPackCompositionSource{Path: name, SourceType: sourceType, DigestSHA256: digest})
	}
	if len(layer.Sources) > 0 {
		layer.Status = v1.AgentPackCompositionApplied
	}
	return nil
}

func (builder *compositionBuilder) agentPack(input compositionInput) map[string]bool {
	skills := map[string]bool{}
	if input.Pack == nil {
		return skills
	}
	builder.body.AgentPack = &v1.AgentPackCompositionPack{
		Version: input.Pack.Version, Digest: input.Pack.Digest, KeyID: input.Pack.KeyID, PluginLoaded: input.PluginLoaded,
	}
	layer := builder.layers[v1.AgentPackCompositionLayerAgentPack]
	layer.Status = v1.AgentPackCompositionApplied
	names := make([]string, 0, len(input.PackFiles))
	for name := range input.PackFiles {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		parts := strings.Split(name, "/")
		if len(parts) > 2 && parts[0] == "skills" {
			skills[parts[1]] = true
		}
		layer.Sources = append(layer.Sources, v1.AgentPackCompositionSource{
			Path: name, SourceType: packSourceType(parts), DigestSHA256: input.PackFiles[name],
		})
	}
	return skills
}

func packSourceType(parts []string) v1.AgentConfigSourceType {
	switch {
	case len(parts) == 2 && parts[0] == ".claude-plugin":
		return v1.AgentConfigProviderConfiguration
	case parts[0] == "skills":
		return v1.AgentConfigSkill
	case parts[0] == "commands":
		return v1.AgentConfigCommand
	case parts[0] == "agents":
		return v1.AgentConfigAgentDefinition
	case parts[0] == "hooks":
		return v1.AgentConfigHook
	case len(parts) == 1 && parts[0] == ".mcp.json":
		return v1.AgentConfigMCPDeclaration
	default:
		return v1.AgentConfigDevelopmentInstruction
	}
}

func adapterInstructionFile(adapter, base string) bool {
	if adapter == "claude" {
		return base == "CLAUDE.md"
	}
	return base == "AGENTS.md"
}

func (builder *compositionBuilder) repository(adapter string, repository compositionRepository, inventory scanner.Inventory, packSkills map[string]bool) {
	repositoryLayer := builder.layers[v1.AgentPackCompositionLayerRepository]
	nestedLayer := builder.layers[v1.AgentPackCompositionLayerNested]
	collisions := map[string]bool{}
	for _, item := range inventory.Items {
		base := path.Base(item.Path)
		nested := strings.Contains(item.Path, "/")
		source := v1.AgentPackCompositionSource{
			RepositoryID: repository.ID, Path: item.Path, SourceType: item.SourceType, DigestSHA256: item.DigestSHA256,
		}
		if item.SourceType == v1.AgentConfigPlatformManifest && strings.HasPrefix(strings.ToLower(base), "manifest.") {
			builder.conflict(v1.AgentPackConflictRepositoryManifestNotApplied, v1.AgentPackCompositionLayerExecutionManifest, repository.ID, item.Path)
		}
		if item.SourceType == v1.AgentConfigRepositoryInstructions && (base == "CLAUDE.md" || base == "AGENTS.md") && !repository.Primary {
			if adapter == "claude" {
				builder.conflict(v1.AgentPackConflictPeerRepositoryInstructions, v1.AgentPackCompositionLayerRepository, repository.ID, item.Path)
			}
			continue
		}
		if adapter == "claude" {
			switch {
			case item.SourceType == v1.AgentConfigProviderConfiguration && item.Provider == "claude":
				builder.conflict(v1.AgentPackConflictRepositorySettingsExcluded, v1.AgentPackCompositionLayerRepository, repository.ID, item.Path)
			case item.SourceType == v1.AgentConfigMCPDeclaration:
				builder.conflict(v1.AgentPackConflictRepositoryMCPExcluded, v1.AgentPackCompositionLayerRepository, repository.ID, item.Path)
			}
		}
		if !repository.Primary {
			continue
		}
		if item.SourceType == v1.AgentConfigRepositoryInstructions && adapterInstructionFile(adapter, base) {
			if nested {
				nestedLayer.Sources = append(nestedLayer.Sources, source)
			} else {
				repositoryLayer.Sources = append(repositoryLayer.Sources, source)
			}
			continue
		}
		providerDirectory := "." + adapter + "/"
		if item.Provider == adapter && strings.HasPrefix(item.Path, providerDirectory) &&
			(item.SourceType == v1.AgentConfigSkill || item.SourceType == v1.AgentConfigCommand || item.SourceType == v1.AgentConfigAgentDefinition) {
			repositoryLayer.Sources = append(repositoryLayer.Sources, source)
			parts := strings.Split(item.Path, "/")
			if adapter == "claude" && item.SourceType == v1.AgentConfigSkill && len(parts) > 3 && packSkills[parts[2]] && !collisions[parts[2]] {
				collisions[parts[2]] = true
				builder.conflict(v1.AgentPackConflictSkillNameCollision, v1.AgentPackCompositionLayerAgentPack, repository.ID, strings.Join(parts[:3], "/"))
			}
		}
	}
	if len(repositoryLayer.Sources) > 0 {
		repositoryLayer.Status = v1.AgentPackCompositionApplied
	}
	if len(nestedLayer.Sources) > 0 {
		nestedLayer.Status = v1.AgentPackCompositionApplied
	}
}

var hostUserConfigurationSources = []struct {
	name       string
	sourceType v1.AgentConfigSourceType
}{
	{"CLAUDE.md", v1.AgentConfigRepositoryInstructions},
	{"plugins", v1.AgentConfigExecutableExtension},
	{"settings.json", v1.AgentConfigProviderConfiguration},
}

func (builder *compositionBuilder) hostUserConfiguration(input compositionInput) {
	layer := builder.layers[v1.AgentPackCompositionLayerHostUser]
	switch {
	case input.Isolation == v1.AgentPackCompositionIsolationDocker:
		layer.Status = v1.AgentPackCompositionExcluded
		return
	case input.Adapter != "claude":
		layer.Status = v1.AgentPackCompositionNotComposed
		return
	}
	claudeDir := input.HostClaudeDir
	if claudeDir == "" && input.HostHome != "" {
		claudeDir = filepath.Join(input.HostHome, ".claude")
	}
	if claudeDir == "" {
		return
	}
	for _, candidate := range hostUserConfigurationSources {
		if _, err := os.Lstat(filepath.Join(claudeDir, candidate.name)); err != nil {
			continue
		}
		label := v1.AgentPackCompositionHostHomeLabel + "/.claude/" + candidate.name
		layer.Sources = append(layer.Sources, v1.AgentPackCompositionSource{Path: label, SourceType: candidate.sourceType})
		builder.conflict(v1.AgentPackConflictHostUserConfigurationLoaded, v1.AgentPackCompositionLayerHostUser, "", label)
	}
	if len(layer.Sources) > 0 {
		layer.Status = v1.AgentPackCompositionAppliedUnisolated
	}
}

func (builder *compositionBuilder) conflict(code, layer, repositoryID, location string) {
	builder.conflicts = append(builder.conflicts, v1.AgentPackCompositionConflict{
		Code: code, Layer: layer, RepositoryID: repositoryID, Path: location,
		Winner: v1.AgentPackCompositionConflictWinners[code],
	})
}

func compositionFileDigest(dir, name string) (string, error) {
	file, err := safeio.OpenRegular(dir, name)
	if err != nil {
		return "", fmt.Errorf("composition digest: %w", err)
	}
	defer func() { _ = file.Close() }()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", fmt.Errorf("composition digest: %w", err)
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

type compositionStartContext struct {
	adapter       string
	isolation     string
	pluginLoaded  bool
	hostHome      string
	hostClaudeDir string
}

func compositionPluginLoaded(args []string) bool {
	for _, argument := range args {
		if argument == "--plugin-dir" {
			return true
		}
	}
	return false
}

func compositionHostHome(environment []string) string {
	for _, entry := range environment {
		if key, value, ok := strings.Cut(entry, "="); ok && key == "HOME" && filepath.IsAbs(value) {
			return value
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}

func compositionHostClaudeDir(environment []string) string {
	directory := ""
	for _, entry := range environment {
		if key, value, ok := strings.Cut(entry, "="); ok && key == "CLAUDE_CONFIG_DIR" && filepath.IsAbs(value) {
			directory = value
		}
	}
	return directory
}

func (jr *jobRunner) verifyCompositionBeforeStart(ctx context.Context, start compositionStartContext) (jobOutcome, bool) {
	if jr.compositionHook != nil {
		if err := jr.compositionHook(ctx); err != nil {
			jr.logger.Error("agent composition step failed", "err", err)
			return jr.startupFailureOutcome(v1.JobFailureRunnerInternal, "agent composition failed"), true
		}
	}
	for _, repository := range jr.repositories {
		current, err := worktreeFingerprint(ctx, repository.WorkDir)
		if err != nil || repository.Fingerprint == "" || current != repository.Fingerprint {
			jr.logger.Error("worktree changed during agent composition", "repository", repository.ID, "err", err)
			return jr.startupFailureOutcome(v1.JobFailureWorkspacePreparation, errWorktreeChangedDuringComposition.Error()), true
		}
	}
	packView := v1.AgentPackCompositionPackViewAbsent
	if jr.packDir != "" {
		if err := verifyAgentPackView(jr.packDir, jr.packFiles); err != nil {
			jr.logger.Error("Agent Pack view changed before provider start", "err", err)
			failure := classifyWorkspacePreparationFailure(newAgentPackMaterializationError(v1.AgentPackFailureArchiveInvalid, false, err))
			outcome := jr.startupFailureOutcome(failure.FailureCode, failure.FailureMessage)
			if outcome.State == v1.JobFinishedFailed {
				outcome.FailureCategory = failure.FailureCategory
				outcome.Retryable = failure.Retryable
			}
			return outcome, true
		}
		packView = v1.AgentPackCompositionPackViewVerified
	}
	input := compositionInput{
		Adapter: start.adapter, Isolation: start.isolation, PluginLoaded: start.pluginLoaded,
		ConfigDir: jr.configDir, HostHome: start.hostHome, HostClaudeDir: start.hostClaudeDir,
	}
	if jr.packDir != "" {
		input.Pack = jr.job.AgentPack
		input.PackFiles = jr.packFiles
	}
	for _, repository := range jr.repositories {
		input.Repositories = append(input.Repositories, compositionRepository{ID: repository.ID, Root: repository.WorkDir, Primary: repository.Primary})
	}
	body, err := buildAgentPackComposition(input)
	if err != nil {
		jr.logger.Error("build composition report", "err", err)
		return jr.startupFailureOutcome(v1.JobFailureRunnerInternal, "composition report could not be built"), true
	}
	jr.composition = &body
	if err := jr.sendComposition(ctx, v1.AgentPackCompositionPhaseBeforeStart, packView); err != nil {
		jr.logger.Error("record composition report", "err", err)
		return jr.startupFailureOutcome(v1.JobFailureRunnerInternal, "composition report could not be recorded"), true
	}
	return jobOutcome{}, false
}

func (jr *jobRunner) publishAfterExitComposition(context.Context) {
	if jr.composition == nil {
		return
	}
	packView := v1.AgentPackCompositionPackViewAbsent
	if jr.packDir != "" {
		packView = v1.AgentPackCompositionPackViewVerified
		if err := verifyAgentPackView(jr.packDir, jr.packFiles); err != nil {
			jr.logger.Warn("Agent Pack view changed during the attempt", "err", err)
			packView = v1.AgentPackCompositionPackViewModified
		}
	}
	eventCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := jr.sendComposition(eventCtx, v1.AgentPackCompositionPhaseAfterExit, packView); err != nil {
		jr.logger.Warn("record after-exit composition report", "err", err)
	}
}

func (jr *jobRunner) sendComposition(ctx context.Context, phase, packView string) error {
	authority := jr.job.ManifestAuthority()
	event := v1.AgentPackCompositionEvent{
		Kind: v1.EventKindAgentPackComposition, ManifestKind: authority.Kind, LeaseID: authority.LeaseID,
		JobID: jr.job.JobID, AttemptID: jr.job.AttemptID, ManifestID: authority.ManifestID,
		ManifestDigest: authority.ManifestDigest, FencingToken: jr.job.FencingToken, ObservedAt: time.Now().UTC(),
	}
	if jr.job.AgentRunAuthority != nil {
		agentRunAuthority := *jr.job.AgentRunAuthority
		event.AgentRunAuthority = &agentRunAuthority
	}
	jr.composition.apply(&event, phase, packView)
	if err := validation.AgentPackCompositionEvent(event); err != nil {
		return err
	}
	_, err := jr.d.sendEventWithStableID(ctx, agentPackCompositionEventID(jr.job.AttemptID, authority.LeaseID, jr.job.FencingToken, phase), event)
	return err
}
