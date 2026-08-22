package v1

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestProviderReviewFeedbackContentGoldenNormalizedTextVector(t *testing.T) {
	content := "Caf\u00e9\nFix the retry path."
	if len([]byte(content)) != 25 {
		t.Fatalf("bytes=%d", len([]byte(content)))
	}
	outcome := ProviderReviewFeedbackContentResultOutcome{Kind: ProviderReviewFeedbackContentSucceeded, RequestState: ProviderReviewObservationRequestOpen, ObservedHeadSHA: strings.Repeat("b", 40), ContentEncoding: "utf-8", ContentFormat: "plain_text", UnicodeNormalization: "NFC", LineEndings: "LF", ContentDigest: "938baec10b632617f4a4779cd8c576ac27dd676d399ba72c8fd180e90f490527", ContentByteCount: 25, Content: content}
	raw, err := json.Marshal(outcome)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{`"content_encoding":"utf-8"`, `"unicode_normalization":"NFC"`, `"content":"Caf`} {
		if !strings.Contains(string(raw), required) {
			t.Fatalf("wire=%s", raw)
		}
	}
}

func TestProviderReviewFeedbackContentErrorWireDoesNotLeakContent(t *testing.T) {
	raw, err := json.Marshal(ProviderReviewFeedbackContentResultOutcome{Kind: ProviderReviewFeedbackContentPermanent, Code: ProviderReviewFeedbackContentReviewEventMetadataChanged, Content: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "secret") || strings.Contains(string(raw), "content") {
		t.Fatalf("wire=%s", raw)
	}
}

func TestProviderReviewFeedbackContentAuthorityUsesExactNativeReviewLocator(t *testing.T) {
	raw, err := json.Marshal(ProviderReviewFeedbackContentAuthority{ContentSource: "provider_review_event", ProviderReviewEventID: "7001", ProviderReviewEventRevision: 3, ReviewEventKind: ProviderReviewObservationChangeRequest, ReviewState: ProviderReviewObservationChangesRequested})
	if err != nil {
		t.Fatal(err)
	}
	wire := string(raw)
	for _, required := range []string{`"content_source":"provider_review_event"`, `"provider_review_event_id":"7001"`, `"review_event_kind":"change_request"`, `"review_state":"changes_requested"`} {
		if !strings.Contains(wire, required) {
			t.Fatalf("wire=%s", wire)
		}
	}
	if strings.Contains(wire, "provider_comment") {
		t.Fatalf("unbound comment locator leaked into protocol: %s", wire)
	}
}
