package daemon

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/example/app-runner/internal/validation"
)

type artifactUploadReceipt struct {
	ReceiptID    string `json:"receiptId"`
	Duplicate    *bool  `json:"duplicate"`
	UploadID     string `json:"uploadId"`
	DigestSHA256 string `json:"digestSha256"`
	SizeBytes    *int64 `json:"sizeBytes"`
}

func verifyArtifactUploadReceipt(body io.Reader, pending pendingArtifactUpload) error {
	raw, err := io.ReadAll(io.LimitReader(body, 4097))
	if err != nil || len(raw) > 4096 {
		return fmt.Errorf("artifact upload receipt is unavailable or oversized")
	}
	if err := rejectDuplicateReceiptFields(raw); err != nil {
		return fmt.Errorf("artifact upload receipt is malformed")
	}
	var receipt artifactUploadReceipt
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&receipt); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return fmt.Errorf("artifact upload receipt is malformed")
	}
	if validation.ID("receipt_id", receipt.ReceiptID) != nil || receipt.Duplicate == nil || receipt.SizeBytes == nil ||
		receipt.UploadID != pending.RecordID || receipt.DigestSHA256 != pending.Artifact.DigestSHA256 || *receipt.SizeBytes != pending.Artifact.SizeBytes {
		return fmt.Errorf("artifact upload receipt does not match queued artifact")
	}
	return nil
}

func rejectDuplicateReceiptFields(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return fmt.Errorf("receipt must be an object")
	}
	seen := map[string]bool{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		key, ok := token.(string)
		if !ok || seen[key] {
			return fmt.Errorf("receipt fields must be unique")
		}
		switch key {
		case "receiptId", "duplicate", "uploadId", "digestSha256", "sizeBytes":
		default:
			return fmt.Errorf("receipt field is unknown")
		}
		seen[key] = true
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return err
		}
	}
	if len(seen) != 5 {
		return fmt.Errorf("receipt fields must be complete")
	}
	return nil
}
