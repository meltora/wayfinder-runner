package v1

const (
	ProtocolFeatureJobSpecWorkAttachments = "job_spec_work_attachments"

	WorkAttachmentTrustUntrustedUser = "untrusted_user_attachment"
	WorkAttachmentUsageContextOnly   = "context_only"

	MaxJobWorkAttachments     = 5
	MaxJobWorkAttachmentBytes = 25 * 1024 * 1024
)

var JobWorkAttachmentMediaTypes = []string{
	"text/plain",
	"text/markdown",
	"image/png",
	"image/jpeg",
	"image/webp",
}

type JobWorkAttachment struct {
	AttachmentID string `json:"attachment_id"`
	Name         string `json:"name"`
	MediaType    string `json:"media_type"`
	SizeBytes    int64  `json:"size_bytes"`
	SHA256       string `json:"sha256"`
	Trust        string `json:"trust"`
	Usage        string `json:"usage"`
}
