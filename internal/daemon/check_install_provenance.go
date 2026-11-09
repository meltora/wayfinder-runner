package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/example/app-runner/internal/isolation"
	"github.com/example/app-runner/internal/safeio"
)

const (
	runnerArtifactPrefix       = "runner-"
	checkProvenanceSchema      = "our-runner.check-install.v2"
	checkProvenanceNamePrefix  = "runner-check-install-"
	checkProvenanceContentType = "application/json"
	checkProvenanceDirectory   = ".check-provenance"
)

type checkProvenance struct {
	Schema             string         `json:"schema"`
	JobID              string         `json:"job_id"`
	AttemptID          string         `json:"attempt_id"`
	CheckID            string         `json:"check_id"`
	RepositoryID       string         `json:"repository_id"`
	CommitSHA          string         `json:"commit_sha"`
	Isolation          string         `json:"isolation"`
	Hermetic           bool           `json:"hermetic"`
	CheckImage         *string        `json:"check_image"`
	CarriedDirectories []string       `json:"carried_directories"`
	Install            *installReport `json:"install"`
	Result             string         `json:"result"`
}

func reservedRunnerArtifactName(name string) bool {
	visible := strings.TrimLeftFunc(strings.Map(dropFormatRune, name), unicode.IsSpace)
	return len(visible) >= len(runnerArtifactPrefix) && asciiLower(visible[:len(runnerArtifactPrefix)]) == runnerArtifactPrefix
}

func dropFormatRune(r rune) rune {
	if unicode.Is(unicode.Cf, r) {
		return -1
	}
	return r
}

func asciiLower(value string) string {
	out := []byte(value)
	for index, char := range out {
		if 'A' <= char && char <= 'Z' {
			out[index] = char + ('a' - 'A')
		}
	}
	return string(out)
}

func checkProvenanceName(checkID string) string {
	return checkProvenanceNamePrefix + checkID + ".json"
}

func (jr *jobRunner) checkProvenanceDocument(checkID, repositoryID, commitSHA string, report checkReport) ([]byte, error) {
	isolationMode := "direct"
	if jr.d.cfg.Isolation == "docker" {
		isolationMode = "docker"
	}
	document := checkProvenance{
		Schema: checkProvenanceSchema, JobID: jr.job.JobID, AttemptID: jr.job.AttemptID,
		CheckID: checkID, RepositoryID: repositoryID, CommitSHA: commitSHA,
		Isolation: isolationMode, Hermetic: isolationMode == "docker", CarriedDirectories: []string{},
		Install: report.install, Result: report.outcome.Result,
	}
	if isolationMode == "docker" {
		document.CheckImage = jr.checkImageDigest(report.checkImage)
	}
	return json.Marshal(document)
}

func (jr *jobRunner) checkImageDigest(checkImage string) *string {
	ref := checkImage
	if ref == "" {
		ref = jr.d.cfg.JobImage
	}
	digest := isolation.ImageDigest(ref)
	if digest == "" {
		return nil
	}
	return &digest
}

func (jr *jobRunner) publishCheckProvenance(ctx context.Context, checkID, repositoryID, commitSHA string, report checkReport) {
	if err := jr.writeAndPublishCheckProvenance(ctx, checkID, repositoryID, commitSHA, report); err != nil {
		jr.logger.Warn("check provenance artifact was not stored", "check", checkID, "err", err)
	}
}

func (jr *jobRunner) writeAndPublishCheckProvenance(ctx context.Context, checkID, repositoryID, commitSHA string, report checkReport) error {
	document, err := jr.checkProvenanceDocument(checkID, repositoryID, commitSHA, report)
	if err != nil {
		return err
	}
	dir := filepath.Join(trustedResultsRoot(jr.d.cfg.StateDir, jr.workDir), checkProvenanceDirectory)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	name := checkProvenanceName(checkID)
	target := filepath.Join(dir, name)
	if err := os.WriteFile(target, document, 0o600); err != nil {
		return err
	}
	file, err := safeio.OpenRegular(dir, name)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	_, err = jr.publishArtifactContent(ctx, name, checkProvenanceContentType, file)
	return err
}
