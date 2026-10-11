package v1

const AgentPackTrustChainVersion = "1"

type AgentPackTrustedKeySet struct {
	DocumentBase64  string `json:"document_base64"`
	SignatureBase64 string `json:"signature_base64"`
	RootKeyID       string `json:"root_key_id"`
}
