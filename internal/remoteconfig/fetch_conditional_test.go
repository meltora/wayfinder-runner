package remoteconfig

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/example/app-runner/internal/mockcp"
)

func TestFetchIfChangedSendsTheRunningDigestAndRecognisesNotModified(t *testing.T) {
	server := startMockControlPlane(t)
	stateDir, _ := enrolledStateDir(t, server)
	fetcher, err := NewFetcher(stateDir, server.WSURL())
	if err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("a", 64)
	server.SetConfigurationResponses(
		mockcp.ConfigurationResponse{Status: http.StatusNotModified},
		mockcp.ConfigurationResponse{Status: http.StatusOK, Body: []byte(`{"ok":true}`)},
	)
	if _, err := fetcher.FetchIfChanged(context.Background(), digest); !errors.Is(err, ErrNotModified) {
		t.Fatalf("304 err = %v", err)
	}
	raw, err := fetcher.FetchIfChanged(context.Background(), digest)
	if err != nil || string(raw) != `{"ok":true}` {
		t.Fatalf("200 raw = %s err = %v", raw, err)
	}
	requests := server.ConfigurationRequests()
	if len(requests) != 2 || requests[0].IfNoneMatch != `"`+digest+`"` || requests[1].IfNoneMatch != `"`+digest+`"` {
		t.Fatalf("requests = %+v", requests)
	}
}

func TestFetchIfChangedOmitsTheHeaderWithoutAValidDigest(t *testing.T) {
	server := startMockControlPlane(t)
	stateDir, _ := enrolledStateDir(t, server)
	fetcher, err := NewFetcher(stateDir, server.WSURL())
	if err != nil {
		t.Fatal(err)
	}
	server.SetConfigurationResponses(mockcp.ConfigurationResponse{Status: http.StatusOK, Body: []byte(`{}`)})
	for _, digest := range []string{"", "not-a-digest", strings.Repeat("A", 64)} {
		if _, err := fetcher.FetchIfChanged(context.Background(), digest); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := fetcher.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, request := range server.ConfigurationRequests() {
		if request.IfNoneMatch != "" {
			t.Fatalf("requests = %+v", server.ConfigurationRequests())
		}
	}
}

func TestNotModifiedIsNeitherTransientNorAStatusFailure(t *testing.T) {
	if IsTransient(ErrNotModified) {
		t.Fatal("not modified is transient")
	}
	var statusErr *StatusError
	if errors.As(ErrNotModified, &statusErr) {
		t.Fatal("not modified is a status error")
	}
}
