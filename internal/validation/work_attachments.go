package validation

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	v1 "github.com/example/app-runner/api/runner/v1"
)

const maxWorkAttachmentNameBytes = 255

var attachmentIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func WorkAttachments(attachments []v1.JobWorkAttachment) error {
	if attachments == nil {
		return nil
	}
	if len(attachments) == 0 || len(attachments) > v1.MaxJobWorkAttachments {
		return fmt.Errorf("work_attachments must list between 1 and %d attachments", v1.MaxJobWorkAttachments)
	}
	ids := map[string]bool{}
	digests := map[string]bool{}
	for _, attachment := range attachments {
		if err := WorkAttachment(attachment); err != nil {
			return err
		}
		if ids[attachment.AttachmentID] || digests[attachment.SHA256] {
			return fmt.Errorf("work attachments must be unique")
		}
		ids[attachment.AttachmentID] = true
		digests[attachment.SHA256] = true
	}
	return nil
}

func WorkAttachment(attachment v1.JobWorkAttachment) error {
	if !attachmentIDPattern.MatchString(attachment.AttachmentID) {
		return fmt.Errorf("work attachment id is invalid")
	}
	if err := workAttachmentName(attachment.Name); err != nil {
		return err
	}
	if !slices.Contains(v1.JobWorkAttachmentMediaTypes, attachment.MediaType) {
		return fmt.Errorf("work attachment media type %q is not supported", attachment.MediaType)
	}
	if attachment.SizeBytes < 1 || attachment.SizeBytes > v1.MaxJobWorkAttachmentBytes {
		return fmt.Errorf("work attachment size is out of range")
	}
	if err := SHA256Digest("work attachment sha256", attachment.SHA256); err != nil {
		return err
	}
	if attachment.Trust != v1.WorkAttachmentTrustUntrustedUser {
		return fmt.Errorf("work attachment trust must be %q", v1.WorkAttachmentTrustUntrustedUser)
	}
	if attachment.Usage != v1.WorkAttachmentUsageContextOnly {
		return fmt.Errorf("work attachment usage must be %q", v1.WorkAttachmentUsageContextOnly)
	}
	return nil
}

func workAttachmentName(name string) error {
	if name == "" || name == "." || name == ".." || len(name) > maxWorkAttachmentNameBytes || !utf8.ValidString(name) {
		return fmt.Errorf("work attachment name is invalid")
	}
	if strings.ContainsAny(name, `/\`) || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return fmt.Errorf("work attachment name is invalid")
	}
	return nil
}
