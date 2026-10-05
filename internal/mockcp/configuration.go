package mockcp

import (
	"net/http"
	"sync"
)

type ConfigurationResponse struct {
	Status int
	Body   []byte
}

type configurationEndpoint struct {
	mu        sync.Mutex
	responses []ConfigurationResponse
	requests  []ConfigurationRequest
}

type ConfigurationRequest struct {
	Method      string
	RawQuery    string
	ClientName  string
	IfNoneMatch string
}

func (s *Server) SetConfigurationResponses(responses ...ConfigurationResponse) {
	s.configuration.mu.Lock()
	defer s.configuration.mu.Unlock()
	s.configuration.responses = append([]ConfigurationResponse(nil), responses...)
}

func (s *Server) ConfigurationRequests() []ConfigurationRequest {
	s.configuration.mu.Lock()
	defer s.configuration.mu.Unlock()
	return append([]ConfigurationRequest(nil), s.configuration.requests...)
}

func (s *Server) handleConfiguration(w http.ResponseWriter, r *http.Request) {
	request := ConfigurationRequest{Method: r.Method, RawQuery: r.URL.RawQuery, IfNoneMatch: r.Header.Get("If-None-Match")}
	if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 {
		request.ClientName = r.TLS.PeerCertificates[0].Subject.CommonName
	}
	s.configuration.mu.Lock()
	s.configuration.requests = append(s.configuration.requests, request)
	response := ConfigurationResponse{Status: http.StatusNotFound, Body: []byte(`{"code":"configuration_not_ready"}`)}
	if len(s.configuration.responses) > 0 {
		response = s.configuration.responses[0]
		if len(s.configuration.responses) > 1 {
			s.configuration.responses = s.configuration.responses[1:]
		}
	}
	s.configuration.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(response.Status)
	_, _ = w.Write(response.Body)
}
