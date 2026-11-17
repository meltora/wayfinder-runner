package scanner

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"unicode/utf8"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/redact"
)

type ContentLimits struct {
	MaxItemBytes    int
	MaxTotalBytes   int
	MaxItems        int
	MaxEncodedBytes int
}

func ItemContents(items []v1.AgentConfigDiscoveryItem, contents map[string]string, limits ContentLimits) []v1.AgentConfigItemContent {
	ordered := make([]v1.AgentConfigDiscoveryItem, 0, len(items))
	for _, item := range items {
		if _, present := contents[item.Path]; present && item.Classification != v1.AgentConfigClassRejected && item.DigestSHA256 != "" {
			ordered = append(ordered, item)
		}
	}
	sort.Slice(ordered, func(left, right int) bool {
		return v1.AgentConfigContentPathLess(ordered[left].Path, ordered[right].Path)
	})
	var selected []v1.AgentConfigItemContent
	total, encoded := 0, 0
	for _, item := range ordered {
		raw := contents[item.Path]
		text := redact.KnownSecrets(raw)
		if text != raw || !utf8.ValidString(text) || len(text) > limits.MaxItemBytes || !matchesDigest(text, item.DigestSHA256) {
			continue
		}
		entry := v1.AgentConfigItemContent{Path: item.Path, Text: text}
		wire, err := json.Marshal(entry)
		if err != nil {
			continue
		}
		size := len(wire) + 1
		if len(selected) == limits.MaxItems || total+len(text) > limits.MaxTotalBytes || encoded+size > limits.MaxEncodedBytes {
			continue
		}
		selected = append(selected, entry)
		total += len(text)
		encoded += size
	}
	return selected
}

func matchesDigest(text, digest string) bool {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:]) == digest
}
