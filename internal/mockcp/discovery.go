package mockcp

import (
	"encoding/json"
	"net/http"
	"sync"
)

type discoveryEndpoint struct {
	mu       sync.Mutex
	response *ConfigurationResponse
	requests []ConfigurationRequest
}

func (s *Server) SetDiscoveryResponse(response ConfigurationResponse) {
	s.discovery.mu.Lock()
	defer s.discovery.mu.Unlock()
	s.discovery.response = &response
}

func (s *Server) DiscoveryRequests() []ConfigurationRequest {
	s.discovery.mu.Lock()
	defer s.discovery.mu.Unlock()
	return append([]ConfigurationRequest(nil), s.discovery.requests...)
}

func (s *Server) handleDiscovery(w http.ResponseWriter, r *http.Request) {
	s.discovery.mu.Lock()
	s.discovery.requests = append(s.discovery.requests, ConfigurationRequest{Method: r.Method, RawQuery: r.URL.RawQuery})
	configured := s.discovery.response
	s.discovery.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if configured != nil {
		w.WriteHeader(configured.Status)
		_, _ = w.Write(configured.Body)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"schema_version": 1, "gateway_url": s.WSURL()})
}
