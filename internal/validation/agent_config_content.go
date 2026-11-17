package validation

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"unicode/utf8"

	v1 "github.com/example/app-runner/api/runner/v1"
)

func AgentConfigContentCapture(capture v1.AgentConfigContentCapture) error {
	if capture.Version != v1.AgentConfigContentCaptureVersion || capture.MaxItemBytes != v1.AgentConfigContentMaxItemBytes || capture.MaxTotalBytes != v1.AgentConfigContentMaxTotalBytes {
		return fmt.Errorf("unsupported content capture request")
	}
	return nil
}

func AgentConfigItemContents(items []v1.AgentConfigDiscoveryItem, contents []v1.AgentConfigItemContent) error {
	if len(contents) > v1.AgentConfigContentMaxItems {
		return fmt.Errorf("item contents exceed the entry limit")
	}
	byPath := make(map[string]v1.AgentConfigDiscoveryItem, len(items))
	for _, item := range items {
		byPath[item.Path] = item
	}
	total := 0
	for index, content := range contents {
		if index > 0 && !v1.AgentConfigContentPathLess(contents[index-1].Path, content.Path) {
			return fmt.Errorf("item contents must have unique paths in lexical order")
		}
		item, found := byPath[content.Path]
		if !found || item.Classification == v1.AgentConfigClassRejected || item.DigestSHA256 == "" {
			return fmt.Errorf("item contents[%d] does not name an accepted item", index)
		}
		if len(content.Text) > v1.AgentConfigContentMaxItemBytes || !utf8.ValidString(content.Text) {
			return fmt.Errorf("item contents[%d] is not bounded UTF-8 text", index)
		}
		sum := sha256.Sum256([]byte(content.Text))
		if hex.EncodeToString(sum[:]) != item.DigestSHA256 {
			return fmt.Errorf("item contents[%d] does not match the item digest", index)
		}
		total += len(content.Text)
	}
	if total > v1.AgentConfigContentMaxTotalBytes {
		return fmt.Errorf("item contents exceed the total byte limit")
	}
	return nil
}
