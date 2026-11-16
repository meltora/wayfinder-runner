package egresspolicy

import (
	"context"
	"fmt"
	"regexp"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/validation"
)

const (
	JobRulesSchemaVersion = 2

	jobRulesRequestKind  = "acquire_job_rules"
	jobRulesResponseKind = "job_rules_held"
)

var containerNamePattern = regexp.MustCompile(`^our-runner-[0-9a-f]{24}$`)

type JobRulesRequest struct {
	NetworkID     string
	NetworkName   string
	PolicyID      string
	ContainerName string
	ApprovedHosts []string
}

type JobRules struct {
	Binding
	ApprovedHosts       []string
	ApprovedHostsDigest string
}

type jobRulesRequest struct {
	SchemaVersion       int       `json:"schema_version"`
	Kind                string    `json:"kind"`
	Nonce               string    `json:"nonce"`
	NetworkID           string    `json:"network_id"`
	NetworkName         string    `json:"network_name"`
	PolicyID            string    `json:"policy_id"`
	ContainerName       string    `json:"container_name"`
	ApprovedHosts       []string  `json:"approved_hosts"`
	ApprovedHostsDigest string    `json:"approved_hosts_digest"`
	RequestedAt         time.Time `json:"-"`
}

type jobRulesResponse struct {
	SchemaVersion                   int       `json:"schema_version"`
	Kind                            string    `json:"kind"`
	Nonce                           string    `json:"nonce"`
	NetworkID                       string    `json:"network_id"`
	PolicyID                        string    `json:"policy_id"`
	ContainerName                   string    `json:"container_name"`
	EnforcementID                   string    `json:"enforcement_id"`
	RulesDigest                     string    `json:"rules_digest"`
	ApprovedHostsDigest             string    `json:"approved_hosts_digest"`
	LeaseID                         string    `json:"lease_id"`
	Effective                       bool      `json:"effective"`
	UnauthorizedReachabilityBlocked bool      `json:"unauthorized_reachability_blocked"`
	ObservedAt                      time.Time `json:"observed_at"`
}

func (client *Client) AcquireJobRules(ctx context.Context, input JobRulesRequest) (*Lease, JobRules, error) {
	hosts := append([]string{}, input.ApprovedHosts...)
	if !networkIDPattern.MatchString(input.NetworkID) || !idPattern.MatchString(input.NetworkName) ||
		!idPattern.MatchString(input.PolicyID) || !containerNamePattern.MatchString(input.ContainerName) {
		return nil, JobRules{}, fmt.Errorf("job egress rules authority is invalid")
	}
	if err := validation.EgressApprovedHosts(hosts); err != nil {
		return nil, JobRules{}, err
	}
	nonce, err := client.prepareExchange()
	if err != nil {
		return nil, JobRules{}, err
	}
	req := jobRulesRequest{
		SchemaVersion: JobRulesSchemaVersion, Kind: jobRulesRequestKind, Nonce: nonce,
		NetworkID: input.NetworkID, NetworkName: input.NetworkName, PolicyID: input.PolicyID,
		ContainerName: input.ContainerName, ApprovedHosts: hosts,
		ApprovedHostsDigest: v1.EgressApprovedHostsDigest(hosts), RequestedAt: client.currentTime().UTC(),
	}
	conn, reader, err := client.exchange(ctx, req)
	if err != nil {
		return nil, JobRules{}, err
	}
	var res jobRulesResponse
	if err := decodeMessage(reader, &res); err != nil {
		_ = conn.Close()
		return nil, JobRules{}, err
	}
	if err := client.validateJobRulesResponse(req, res); err != nil {
		_ = conn.Close()
		return nil, JobRules{}, err
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		_ = conn.Close()
		return nil, JobRules{}, err
	}
	binding := Binding{NetworkID: res.NetworkID, PolicyID: res.PolicyID, EnforcementID: res.EnforcementID, RulesDigest: res.RulesDigest}
	lease := client.holdLease(conn, reader, JobRulesSchemaVersion, binding, res.LeaseID, res.ObservedAt, time.Time{})
	return lease, JobRules{Binding: binding, ApprovedHosts: hosts, ApprovedHostsDigest: req.ApprovedHostsDigest}, nil
}

func (client *Client) validateJobRulesResponse(req jobRulesRequest, res jobRulesResponse) error {
	now := client.currentTime().UTC()
	if res.SchemaVersion != JobRulesSchemaVersion || res.Kind != jobRulesResponseKind || res.Nonce != req.Nonce ||
		res.NetworkID != req.NetworkID || res.PolicyID != req.PolicyID || res.ContainerName != req.ContainerName ||
		res.ApprovedHostsDigest != req.ApprovedHostsDigest ||
		!idPattern.MatchString(res.EnforcementID) || !digestPattern.MatchString(res.RulesDigest) || !idPattern.MatchString(res.LeaseID) ||
		!res.Effective || !res.UnauthorizedReachabilityBlocked || res.ObservedAt.IsZero() ||
		res.ObservedAt.Before(req.RequestedAt) || res.ObservedAt.After(now.Add(2*time.Second)) {
		return fmt.Errorf("egress policy verifier did not hold fresh effective job egress rules")
	}
	return nil
}
