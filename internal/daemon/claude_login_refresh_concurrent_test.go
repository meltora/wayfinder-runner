package daemon

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/example/app-runner/internal/claudelogin"
)

const (
	refreshChildDirEnv = "RUNNER_TEST_REFRESH_CHILD_DIR"
	refreshChildURLEnv = "RUNNER_TEST_REFRESH_CHILD_URL"
)

type rotatingTokenEndpoint struct {
	mu       sync.Mutex
	current  string
	used     map[string]bool
	next     int
	rotated  int
	reused   int
	inFlight int
	overlap  bool
}

func (e *rotatingTokenEndpoint) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	token := string(body)
	e.mu.Lock()
	e.inFlight++
	if e.inFlight > 1 {
		e.overlap = true
	}
	e.mu.Unlock()
	time.Sleep(300 * time.Millisecond)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.inFlight--
	if e.used[token] || token != e.current {
		e.reused++
		e.current = ""
		http.Error(w, "invalid_grant", http.StatusBadRequest)
		return
	}
	e.used[token] = true
	e.next++
	name := fmt.Sprintf("r%d", e.next)
	e.current = "fake-" + name
	e.rotated++
	_, _ = io.WriteString(w, fakeCredentials(fakeOAuth(name, time.Now().Add(8*time.Hour).UnixMilli(), "")))
}

func TestClaudeLoginRefreshChildProcess(t *testing.T) {
	dir := os.Getenv(refreshChildDirEnv)
	url := os.Getenv(refreshChildURLEnv)
	if dir == "" || url == "" {
		t.Skip("runs only as a child of the concurrent refresh test")
	}
	logs := &bytes.Buffer{}
	refresh := func(ctx context.Context, token string, _ []string) ([]byte, error) {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(token))
		if err != nil {
			return nil, err
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			return nil, err
		}
		defer func() { _ = response.Body.Close() }()
		out, err := io.ReadAll(response.Body)
		if err != nil {
			return nil, err
		}
		if response.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("token endpoint status %d", response.StatusCode)
		}
		return out, nil
	}
	s := newClaudeLoginSync(nil, refresh, slog.New(slog.NewTextHandler(logs, nil)))
	s.lock = claudelogin.LockOptions{Retry: 5 * time.Millisecond, Timeout: 5 * time.Second}
	s.refreshHost(context.Background(), dir)
	fmt.Fprint(os.Stderr, logs.String())
}

func TestClaudeLoginRefreshThreeProcessesRotateTheHostLoginOnce(t *testing.T) {
	endpoint := &rotatingTokenEndpoint{current: "fake-r0", used: map[string]bool{}}
	server := httptest.NewServer(endpoint)
	defer server.Close()
	dir := filepath.Join(t.TempDir(), "claude-personal")
	writeTestFile(t, filepath.Join(dir, ".credentials.json"), `{"mcpOAuth":{"server":{"accessToken":"fake-mcp"}},"claudeAiOauth":`+fakeOAuth("r0", time.Now().Add(10*time.Minute).UnixMilli(), "")+`}`)

	var wg sync.WaitGroup
	outputs := make([]bytes.Buffer, 3)
	errs := make([]error, 3)
	for i := range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := exec.Command(os.Args[0], "-test.run=^TestClaudeLoginRefreshChildProcess$", "-test.count=1")
			cmd.Env = append(os.Environ(), refreshChildDirEnv+"="+dir, refreshChildURLEnv+"="+server.URL)
			cmd.Stdout = &outputs[i]
			cmd.Stderr = &outputs[i]
			errs[i] = cmd.Run()
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("child %d failed: %v\n%s", i, err, outputs[i].String())
		}
	}

	endpoint.mu.Lock()
	rotated, reused, overlap, current := endpoint.rotated, endpoint.reused, endpoint.overlap, endpoint.current
	endpoint.mu.Unlock()
	logs := outputs[0].String() + outputs[1].String() + outputs[2].String()
	if reused != 0 || overlap {
		t.Fatalf("a refresh token was presented concurrently or twice (reused=%d overlap=%v)\n%s", reused, overlap, logs)
	}
	if rotated != 1 {
		t.Fatalf("rotations = %d, want 1\n%s", rotated, logs)
	}
	data, host, err := claudelogin.ReadLogin(dir)
	if err != nil {
		t.Fatalf("host login unusable after the refresh: %v", err)
	}
	if !host.HasRefresh || current != "fake-r1" || !strings.Contains(string(data), `"refreshToken":"fake-r1"`) || !strings.Contains(string(data), `"accessToken":"fake-access-r1"`) {
		t.Fatalf("host login does not hold the rotated grant")
	}
	if !strings.Contains(string(data), `"mcpOAuth"`) {
		t.Fatal("the refresh dropped other credentials")
	}
	if strings.Contains(logs, "fake-") {
		t.Fatal("a token value was logged")
	}
}
