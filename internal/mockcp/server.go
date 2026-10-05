package mockcp

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	v1 "github.com/example/app-runner/api/runner/v1"
)

type Server struct {
	CA *TestCA

	enrollSrv *httptestServer
	wsSrv     *httptestServer

	mu           sync.Mutex
	events       []v1.Envelope
	commandAcks  []v1.Envelope
	heartbeats   int
	helloCount   int
	DropOnHello  int
	enrollCodes  map[string]bool
	enrolledCNs  map[string]bool
	nextRunnerID int
	conn         *websocket.Conn
	connMu       sync.Mutex
	currentSeq   int
	packs        map[string][]byte
	artifacts    map[string][]byte

	configuration configurationEndpoint
	discovery     discoveryEndpoint

	certLifetime     time.Duration
	refuseActivation bool
	rotateFailures   []RotateFailure
	runnerCerts      map[string]*runnerCertificates
	rotateRequests   []RotateRequest
	activations      int
	streamRejections int
	streamLog        []string
	connSeq          int
	streamGate       chan struct{}
}

type RotateFailure struct {
	Status int
	Code   string
}

type RotateRequest struct {
	RunnerID    string
	SPKISHA256  string
	Status      int
	Certificate string
}

type runnerCertificates struct {
	current    string
	pending    *pendingCertificate
	superseded map[string]bool
	spkis      map[string]bool
}

type pendingCertificate struct {
	fingerprint string
	spki        string
	certPEM     []byte
}

type httptestServer struct {
	listener net.Listener
	server   *http.Server
	URL      string
}

func NewServer() (*Server, error) {
	ca, err := NewTestCA()
	if err != nil {
		return nil, err
	}
	s := &Server{CA: ca, enrollCodes: map[string]bool{}, enrolledCNs: map[string]bool{}, nextRunnerID: 1, packs: map[string][]byte{}, artifacts: map[string][]byte{}, runnerCerts: map[string]*runnerCertificates{}}
	return s, nil
}

func NewServerWithCertificateLifetime(lifetime time.Duration) (*Server, error) {
	ca, err := NewTestCAWithLifetime(lifetime + 24*time.Hour)
	if err != nil {
		return nil, err
	}
	return &Server{CA: ca, enrollCodes: map[string]bool{}, enrolledCNs: map[string]bool{}, nextRunnerID: 1, packs: map[string][]byte{}, artifacts: map[string][]byte{}, runnerCerts: map[string]*runnerCertificates{}, certLifetime: lifetime}, nil
}

func (s *Server) issue(csrPEM []byte, cn string) ([]byte, error) {
	lifetime := s.certLifetime
	if lifetime == 0 {
		lifetime = 24 * time.Hour
	}
	return s.CA.SignCSRWithLifetime(csrPEM, cn, lifetime)
}

func (s *Server) SetStreamGate(gate chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.streamGate = gate
}

func (s *Server) SetDropOnHello(count int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.DropOnHello = count
}

func (s *Server) SetRefuseActivation(refuse bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refuseActivation = refuse
}

func (s *Server) FailNextRotation(status int, code string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rotateFailures = append(s.rotateFailures, RotateFailure{Status: status, Code: code})
}

func (s *Server) RotateRequests() []RotateRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]RotateRequest, len(s.rotateRequests))
	copy(out, s.rotateRequests)
	return out
}

func (s *Server) Activations() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.activations
}

func (s *Server) StreamRejections() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.streamRejections
}

func (s *Server) StreamLog() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.streamLog))
	copy(out, s.streamLog)
	return out
}

func (s *Server) PendingCertificate(runnerID string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.runnerCerts[runnerID]
	if state == nil || state.pending == nil {
		return "", false
	}
	return string(state.pending.certPEM), true
}

func (s *Server) CurrentFingerprint(runnerID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if state := s.runnerCerts[runnerID]; state != nil {
		return state.current
	}
	return ""
}

func certificateFingerprint(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.Raw)
	return hex.EncodeToString(sum[:])
}

func spkiHash(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func pemCertificate(certPEM []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return nil, fmt.Errorf("invalid certificate PEM")
	}
	return x509.ParseCertificate(block.Bytes)
}

func (s *Server) recordCurrentLocked(runnerID string, cert *x509.Certificate) *runnerCertificates {
	state := s.runnerCerts[runnerID]
	if state == nil {
		state = &runnerCertificates{superseded: map[string]bool{}, spkis: map[string]bool{}}
		s.runnerCerts[runnerID] = state
	}
	state.current = certificateFingerprint(cert)
	state.spkis[spkiHash(cert.RawSubjectPublicKeyInfo)] = true
	return state
}

func (s *Server) authorizeStreamLocked(cert *x509.Certificate) int {
	state := s.runnerCerts[cert.Subject.CommonName]
	if state == nil {
		return 0
	}
	fingerprint := certificateFingerprint(cert)
	if state.superseded[fingerprint] {
		return http.StatusUnauthorized
	}
	if state.pending != nil && state.pending.fingerprint == fingerprint {
		if s.refuseActivation {
			return http.StatusUnauthorized
		}
		if state.current != "" {
			state.superseded[state.current] = true
		}
		state.current = fingerprint
		state.pending = nil
		s.activations++
	}
	return 0
}

func writeRotationError(w http.ResponseWriter, status int, code string) {
	if code == "" {
		w.WriteHeader(status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code, "error": "Runner certificate rotation could not be completed"})
}

func (s *Server) Artifact(jobID, attemptID, name string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.artifacts[jobID+"/"+attemptID+"/"+name]
	return data, ok
}

func (s *Server) ServePack(name string, data []byte) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.packs[name] = data
	return "/packs/" + name
}

func (s *Server) AddEnrollmentCode(code string) { s.enrollCodes[code] = true }

func (s *Server) Start(ctx context.Context) error {
	if err := s.startEnroll(); err != nil {
		return err
	}
	return s.startWS()
}

func (s *Server) startEnroll() error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/runners/enroll", s.handleEnroll)
	mux.HandleFunc("/v1/runners/discovery", s.handleDiscovery)
	mux.HandleFunc("/v1/runners/rotate", s.handleRotate)
	mux.HandleFunc("/v1/artifacts/", s.handleArtifactUpload)
	mux.HandleFunc("/packs/", s.handlePack)
	mux.HandleFunc("/admin/commands", s.handleAdminCommand)
	mux.HandleFunc("/admin/events", s.handleAdminEvents)
	mux.HandleFunc("/admin/decisions", s.handleAdminDecision)
	s.enrollSrv = &httptestServer{listener: ln, server: &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}, URL: "http://" + ln.Addr().String()}
	go func() { _ = s.enrollSrv.server.Serve(ln) }()
	return nil
}

func (s *Server) handleEnroll(w http.ResponseWriter, r *http.Request) {
	var req struct {
		EnrollmentCode string `json:"enrollment_code"`
		CSR            string `json:"csr"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	valid := s.enrollCodes[req.EnrollmentCode]
	if valid {
		delete(s.enrollCodes, req.EnrollmentCode)
		s.nextRunnerID++
	}
	runnerID := fmt.Sprintf("runner-%d", s.nextRunnerID)
	s.mu.Unlock()
	if !valid {
		http.Error(w, `{"error":"invalid_code"}`, http.StatusForbidden)
		return
	}
	certPEM, err := s.issue([]byte(req.CSR), runnerID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	issued, err := pemCertificate(certPEM)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.mu.Lock()
	s.enrolledCNs[runnerID] = true
	s.recordCurrentLocked(runnerID, issued)
	s.mu.Unlock()
	_ = json.NewEncoder(w).Encode(map[string]string{
		"runner_id":      runnerID,
		"certificate":    string(certPEM),
		"ca_certificate": string(s.CA.CertPEM),
	})
}

func (s *Server) handleRotate(w http.ResponseWriter, r *http.Request) {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		writeRotationError(w, http.StatusUnauthorized, "")
		return
	}
	peer := r.TLS.PeerCertificates[0]
	runnerID := peer.Subject.CommonName
	record := RotateRequest{RunnerID: runnerID}
	defer func() {
		s.mu.Lock()
		s.rotateRequests = append(s.rotateRequests, record)
		s.mu.Unlock()
	}()
	var req struct {
		CSR           string `json:"csr"`
		RunnerVersion string `json:"runner_version"`
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, 16<<10))
	decoder.DisallowUnknownFields()
	if r.Method != http.MethodPost || decoder.Decode(&req) != nil || req.CSR == "" {
		record.Status = http.StatusBadRequest
		writeRotationError(w, record.Status, "rotation_request_invalid")
		return
	}
	block, _ := pem.Decode([]byte(req.CSR))
	if block == nil {
		record.Status = http.StatusBadRequest
		writeRotationError(w, record.Status, "rotation_request_invalid")
		return
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil || csr.CheckSignature() != nil {
		record.Status = http.StatusBadRequest
		writeRotationError(w, record.Status, "rotation_request_invalid")
		return
	}
	record.SPKISHA256 = spkiHash(csr.RawSubjectPublicKeyInfo)
	s.mu.Lock()
	if !s.enrolledCNs[runnerID] {
		s.mu.Unlock()
		record.Status = http.StatusForbidden
		writeRotationError(w, record.Status, "runner_unavailable")
		return
	}
	state := s.runnerCerts[runnerID]
	if state == nil {
		state = s.recordCurrentLocked(runnerID, peer)
	}
	if state.current != certificateFingerprint(peer) {
		s.mu.Unlock()
		record.Status = http.StatusUnauthorized
		writeRotationError(w, record.Status, "")
		return
	}
	if len(s.rotateFailures) > 0 {
		failure := s.rotateFailures[0]
		s.rotateFailures = s.rotateFailures[1:]
		s.mu.Unlock()
		record.Status = failure.Status
		writeRotationError(w, failure.Status, failure.Code)
		return
	}
	var certPEM []byte
	switch {
	case state.pending != nil && state.pending.spki == record.SPKISHA256:
		certPEM = state.pending.certPEM
	case state.spkis[record.SPKISHA256]:
		s.mu.Unlock()
		record.Status = http.StatusConflict
		writeRotationError(w, record.Status, "rotation_key_reused")
		return
	default:
		certPEM, err = s.issue([]byte(req.CSR), runnerID)
		if err != nil {
			s.mu.Unlock()
			record.Status = http.StatusServiceUnavailable
			writeRotationError(w, record.Status, "rotation_unavailable")
			return
		}
		issued, parseErr := pemCertificate(certPEM)
		if parseErr != nil {
			s.mu.Unlock()
			record.Status = http.StatusServiceUnavailable
			writeRotationError(w, record.Status, "rotation_unavailable")
			return
		}
		state.pending = &pendingCertificate{fingerprint: certificateFingerprint(issued), spki: record.SPKISHA256, certPEM: certPEM}
		state.spkis[record.SPKISHA256] = true
	}
	s.mu.Unlock()
	record.Status = http.StatusOK
	record.Certificate = string(certPEM)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"runner_id":      runnerID,
		"certificate":    string(certPEM),
		"ca_certificate": string(s.CA.CertPEM),
	})
}

func (s *Server) handleArtifactUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/artifacts/"), "/")
	if len(parts) != 3 {
		http.Error(w, "bad path", http.StatusBadRequest)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 256<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	wantDigest := r.Header.Get("X-Artifact-Digest")
	sum := sha256.Sum256(body)
	if wantDigest != "" && hex.EncodeToString(sum[:]) != wantDigest {
		http.Error(w, "digest mismatch", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.artifacts[strings.Join(parts, "/")] = body
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"receiptId": "receipt-" + hex.EncodeToString(sum[:]), "duplicate": false,
		"uploadId": r.Header.Get("X-Artifact-Upload-ID"), "digestSha256": hex.EncodeToString(sum[:]), "sizeBytes": len(body),
	})
}

func (s *Server) handlePack(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	data, ok := s.packs[r.URL.Path[len("/packs/"):]]
	s.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	_, _ = w.Write(data)
}

func (s *Server) handleAdminCommand(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var cmd v1.CommandPayload
	if err := json.NewDecoder(r.Body).Decode(&cmd); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.SendCommand(r.Context(), cmd); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) handleAdminEvents(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	events := make([]v1.Envelope, len(s.events))
	copy(events, s.events)
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(events)
}

func (s *Server) handleAdminDecision(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		QuestionID   string `json:"question_id"`
		Decision     string `json:"decision"`
		DecidedBy    string `json:"decided_by"`
		JobID        string `json:"job_id"`
		AttemptID    string `json:"attempt_id"`
		FencingToken uint64 `json:"fencing_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if body.QuestionID == "" || body.Decision == "" || body.DecidedBy == "" ||
		body.JobID == "" || body.AttemptID == "" || body.FencingToken == 0 {
		http.Error(w, "question, decision, responder, and exact job authority required", http.StatusBadRequest)
		return
	}
	if err := s.SendCommand(r.Context(), v1.CommandPayload{
		Command: v1.CmdDecisionResponse, JobID: body.JobID, AttemptID: body.AttemptID,
		FencingToken: body.FencingToken, QuestionID: body.QuestionID,
		Decision: body.Decision, DecidedBy: body.DecidedBy,
	}); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) startWS() error {
	certPEM, keyPEM, err := s.CA.ServerCert()
	if err != nil {
		return err
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return err
	}
	caPool := x509.NewCertPool()
	caPool.AppendCertsFromPEM(s.CA.CertPEM)
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientCAs:    caPool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS13,
	})
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/runners/stream", s.handleStream)
	mux.HandleFunc("/v1/runners/configuration", s.handleConfiguration)
	mux.HandleFunc("/v1/runners/rotate", s.handleRotate)
	mux.HandleFunc("/v1/artifacts/", s.handleArtifactUpload)
	mux.HandleFunc("/packs/", s.handlePack)
	s.wsSrv = &httptestServer{listener: ln, server: &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}, URL: "wss://" + ln.Addr().String() + "/v1/runners/stream"}
	go func() { _ = s.wsSrv.server.Serve(ln) }()
	return nil
}

func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	gate := s.streamGate
	if gate != nil {
		s.streamLog = append(s.streamLog, "gate:waiting")
	}
	s.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-r.Context().Done():
			return
		}
	}
	if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 {
		s.mu.Lock()
		status := s.authorizeStreamLocked(r.TLS.PeerCertificates[0])
		if status != 0 {
			s.streamRejections++
			s.streamLog = append(s.streamLog, fmt.Sprintf("rejected:%d", status))
		}
		s.mu.Unlock()
		if status != 0 {
			w.WriteHeader(status)
			return
		}
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()
	ctx := r.Context()

	s.mu.Lock()
	s.helloCount++
	helloN := s.helloCount
	drop := s.DropOnHello
	s.connSeq++
	seq := s.connSeq
	s.streamLog = append(s.streamLog, fmt.Sprintf("%d:opened", seq))
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.streamLog = append(s.streamLog, fmt.Sprintf("%d:closed", seq))
		s.mu.Unlock()
	}()

	var hello v1.Envelope
	if err := read(conn, ctx, &hello); err != nil || hello.Type != v1.TypeHello {
		return
	}
	if drop > 0 && helloN <= drop {
		_ = conn.Close(websocket.StatusInternalError, "drop")
		return
	}
	_ = write(conn, ctx, v1.Envelope{
		Version: v1.ProtocolVersion, Type: v1.TypeHelloAck, ID: "ack-" + hello.ID,
		SentAt: time.Now(), InReplyTo: hello.ID,
		Payload: v1.HelloAckPayload{Accepted: true, HeartbeatInterval: 1},
	})
	s.connMu.Lock()
	s.conn = conn
	s.currentSeq = seq
	s.connMu.Unlock()
	defer func() {
		s.connMu.Lock()
		if s.conn == conn {
			s.conn = nil
		}
		s.connMu.Unlock()
	}()
	for {
		var env v1.Envelope
		if err := read(conn, ctx, &env); err != nil {
			return
		}
		s.mu.Lock()
		switch env.Type {
		case v1.TypeEvent:
			s.events = append(s.events, env)
			_ = write(conn, ctx, v1.Envelope{
				Version: v1.ProtocolVersion, Type: v1.TypeEventAck, ID: "ack-" + env.ID,
				SentAt: time.Now(), InReplyTo: env.ID,
			})
		case v1.TypeHeartbeat:
			s.heartbeats++
			_ = write(conn, ctx, v1.Envelope{
				Version: v1.ProtocolVersion, Type: v1.TypeHeartbeatAck, ID: "ack-" + env.ID,
				SentAt: time.Now(), InReplyTo: env.ID,
			})
		case v1.TypeCommandAck:
			s.commandAcks = append(s.commandAcks, env)
			s.streamLog = append(s.streamLog, fmt.Sprintf("%d:command_ack:%s", seq, env.InReplyTo))
		}
		s.mu.Unlock()
	}
}

func (s *Server) EnrollURL() string { return s.enrollSrv.URL }
func (s *Server) WSURL() string     { return s.wsSrv.URL }
func (s *Server) SecureHTTPURL() string {
	return strings.TrimSuffix(strings.Replace(s.wsSrv.URL, "wss://", "https://", 1), "/v1/runners/stream")
}

func (s *Server) SendCommand(ctx context.Context, cmd v1.CommandPayload) error {
	return s.SendCommandEnvelope(ctx, v1.Envelope{
		Version: v1.ProtocolVersion, Type: v1.TypeCommand, ID: fmt.Sprintf("cmd-%d", time.Now().UnixNano()),
		SentAt: time.Now(), Payload: cmd,
	})
}

func (s *Server) SendCommandEnvelope(ctx context.Context, envelope v1.Envelope) error {
	s.connMu.Lock()
	conn := s.conn
	seq := s.currentSeq
	s.connMu.Unlock()
	if conn == nil {
		return fmt.Errorf("no runner connected")
	}
	s.mu.Lock()
	s.streamLog = append(s.streamLog, fmt.Sprintf("%d:command:%s", seq, envelope.ID))
	s.mu.Unlock()
	return write(conn, ctx, envelope)
}

func (s *Server) SendCommandWithProviderCredential(
	ctx context.Context,
	envelope v1.Envelope,
	credential v1.ProviderCredential,
) error {
	envelope.ProviderCredential = nil
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	var frame map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &frame); err != nil {
		return err
	}
	rawCredential, err := json.Marshal(map[string]any{
		"kind": credential.Kind, "token": credential.Token,
		"expires_at": credential.ExpiresAt, "repository_ids": credential.RepositoryIDs,
	})
	if err != nil {
		return err
	}
	frame["provider_credential"] = rawCredential
	data, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	return s.SendRawCommand(ctx, data)
}

func (s *Server) SendRawCommand(ctx context.Context, frame []byte) error {
	s.connMu.Lock()
	conn := s.conn
	s.connMu.Unlock()
	if conn == nil {
		return fmt.Errorf("no runner connected")
	}
	return conn.Write(ctx, websocket.MessageText, frame)
}

func (s *Server) CommandAcks() []v1.Envelope {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]v1.Envelope, len(s.commandAcks))
	copy(out, s.commandAcks)
	return out
}

func (s *Server) EventsByKind(kind string) []v1.Envelope {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []v1.Envelope
	for _, e := range s.events {
		if payload, ok := e.Payload.(map[string]any); ok && payload["kind"] == kind {
			out = append(out, e)
		}
	}
	return out
}

func (s *Server) Events() []v1.Envelope {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]v1.Envelope, len(s.events))
	copy(out, s.events)
	return out
}

func (s *Server) Heartbeats() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.heartbeats
}

func (s *Server) HelloCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.helloCount
}

func (s *Server) Close() {
	if s.enrollSrv != nil {
		_ = s.enrollSrv.server.Close()
		_ = s.enrollSrv.listener.Close()
	}
	if s.wsSrv != nil {
		_ = s.wsSrv.server.Close()
		_ = s.wsSrv.listener.Close()
	}
}

func read(conn *websocket.Conn, ctx context.Context, env *v1.Envelope) error {
	_, data, err := conn.Read(ctx)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, env)
}

func write(conn *websocket.Conn, ctx context.Context, env v1.Envelope) error {
	data, err := json.Marshal(env)
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, data)
}
