package agentpack

import v1 "github.com/example/app-runner/api/runner/v1"

func VerifyPublishRequest(request PublishRequest, publicKeySPKIBase64 string) error {
	authority := v1.SignedAgentPackAuthority{
		SchemaVersion: v1.AgentPackAuthoritySchemaVersion, RegistryVersionID: "unpublished",
		Scope:   v1.AgentPackAuthorityScope{Kind: request.Scope.Kind, OrganizationID: request.Scope.OrganizationID},
		PackKey: request.PackKey, PackName: request.PackName, Provider: request.Provider,
		ProtocolVersion: request.ProtocolVersion, MinimumRunnerVersion: request.MinimumRunnerVersion,
		AgentPack: request.AgentPack, SignatureBase64: request.SignatureBase64,
	}
	digest, err := PublicationAttestationDigest(authority)
	if err != nil {
		return newDeliveryAuthorityError(DeliveryAuthorityInvalid, err)
	}
	authority.AttestationDigest = digest
	return VerifyDeliveryAuthority(authority, request.AgentPack, publicKeySPKIBase64, request.Provider, request.MinimumRunnerVersion)
}
