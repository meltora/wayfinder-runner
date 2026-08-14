package mockcp

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
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
	packs        map[string][]byte
	artifacts    map[string][]byte
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
	s := &Server{CA: ca, enrollCodes: map[string]bool{}, enrolledCNs: map[string]bool{}, nextRunnerID: 1, packs: map[string][]byte{}, artifacts: map[string][]byte{}}
	return s, nil
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
	certPEM, err := s.CA.SignCSR([]byte(req.CSR), runnerID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.enrolledCNs[runnerID] = true
	s.mu.Unlock()
	_ = json.NewEncoder(w).Encode(map[string]string{
		"runner_id":      runnerID,
		"certificate":    string(certPEM),
		"ca_certificate": string(s.CA.CertPEM),
	})
}

func (s *Server) handleRotate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CSR string `json:"csr"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		http.Error(w, "current mTLS identity required", http.StatusUnauthorized)
		return
	}
	oldCert := r.TLS.PeerCertificates[0]
	s.mu.Lock()
	enrolled := s.enrolledCNs[oldCert.Subject.CommonName]
	s.mu.Unlock()
	if !enrolled {
		http.Error(w, `{"error":"unknown_runner"}`, http.StatusForbidden)
		return
	}
	certPEM, err := s.CA.SignCSR([]byte(req.CSR), oldCert.Subject.CommonName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]string{
		"runner_id":      oldCert.Subject.CommonName,
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
	w.WriteHeader(http.StatusCreated)
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
	mux.HandleFunc("/v1/runners/rotate", s.handleRotate)
	s.wsSrv = &httptestServer{listener: ln, server: &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}, URL: "wss://" + ln.Addr().String() + "/v1/runners/stream"}
	go func() { _ = s.wsSrv.server.Serve(ln) }()
	return nil
}

func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
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
	s.mu.Unlock()

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
		}
		s.mu.Unlock()
	}
}

func (s *Server) EnrollURL() string { return s.enrollSrv.URL }
func (s *Server) WSURL() string     { return s.wsSrv.URL }

func (s *Server) SendCommand(ctx context.Context, cmd v1.CommandPayload) error {
	return s.SendCommandEnvelope(ctx, v1.Envelope{
		Version: v1.ProtocolVersion, Type: v1.TypeCommand, ID: fmt.Sprintf("cmd-%d", time.Now().UnixNano()),
		SentAt: time.Now(), Payload: cmd,
	})
}

func (s *Server) SendCommandEnvelope(ctx context.Context, envelope v1.Envelope) error {
	s.connMu.Lock()
	conn := s.conn
	s.connMu.Unlock()
	if conn == nil {
		return fmt.Errorf("no runner connected")
	}
	return write(conn, ctx, envelope)
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
