package previewlifecycle

import (
	"crypto/x509"
	"fmt"
	"net/url"
	"strings"
	"time"

	v1 "github.com/example/app-runner/api/runner/v1"
	"github.com/example/app-runner/internal/config"
	"github.com/example/app-runner/internal/safeio"
)

const (
	maxAuthorizationBytes = 8 << 10
	maxTrustedCABytes     = 1 << 20
)

type controller struct {
	capability        v1.PreviewLifecycleProfileCapability
	controllerURL     *url.URL
	trustedCAFile     string
	authorizationFile string
	timeout           time.Duration
	maxResponseBytes  int64
	rootCAs           *x509.CertPool
	authorization     string
}

func compileController(value config.PreviewLifecycleControllerConfig) (controller, error) {
	_, err := loadTrustedCAs(value.TrustedCAFile)
	if err != nil {
		return controller{}, err
	}
	_, err = loadAuthorization(value.AuthorizationFile)
	if err != nil {
		return controller{}, err
	}
	parsed, err := url.Parse(value.ControllerURL)
	if err != nil {
		return controller{}, fmt.Errorf("parse preview lifecycle controller URL: %w", err)
	}
	actions := make([]v1.PreviewLifecycleAction, len(value.Actions))
	for index, action := range value.Actions {
		actions[index] = v1.PreviewLifecycleAction(action)
	}
	capability := v1.PreviewLifecycleProfileCapability{
		BlueprintID: value.BlueprintID, Version: value.Version, Digest: value.Digest,
		ControllerProtocolVersion: v1.PreviewLifecycleControllerProtocolVersion,
		Actions:                   actions,
	}
	if hasAction(actions, v1.PreviewLifecycleExtend) {
		maxExtensionMS := value.MaxExtensionMS
		capability.MaxExtensionMS = &maxExtensionMS
	}
	return controller{
		capability: capability, controllerURL: parsed,
		trustedCAFile: value.TrustedCAFile, authorizationFile: value.AuthorizationFile,
		timeout: time.Duration(value.TimeoutSeconds) * time.Second, maxResponseBytes: int64(value.MaxResponseBytes),
	}, nil
}

func (compiled controller) withCurrentSecrets() (controller, error) {
	rootCAs, err := loadTrustedCAs(compiled.trustedCAFile)
	if err != nil {
		return controller{}, err
	}
	authorization, err := loadAuthorization(compiled.authorizationFile)
	if err != nil {
		return controller{}, err
	}
	compiled.rootCAs = rootCAs
	compiled.authorization = authorization
	return compiled, nil
}

func loadTrustedCAs(path string) (*x509.CertPool, error) {
	if path == "" {
		return nil, nil
	}
	raw, err := safeio.ReadRegularFile(path, maxTrustedCABytes, 0o022)
	if err != nil {
		return nil, fmt.Errorf("read preview lifecycle trust root: %w", err)
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(raw) {
		return nil, fmt.Errorf("preview lifecycle trust root contains no certificates")
	}
	return pool, nil
}

func loadAuthorization(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	raw, err := safeio.ReadRegularFile(path, maxAuthorizationBytes, 0o077)
	if err != nil {
		return "", fmt.Errorf("read private preview lifecycle credential: %w", err)
	}
	value := strings.TrimSuffix(strings.TrimSuffix(string(raw), "\n"), "\r")
	if len(value) < 8 || len(value) > maxAuthorizationBytes || strings.TrimSpace(value) != value {
		return "", fmt.Errorf("preview lifecycle credential has an invalid format")
	}
	for _, character := range []byte(value) {
		if character < 0x20 || character > 0x7e {
			return "", fmt.Errorf("preview lifecycle credential has an invalid format")
		}
	}
	return value, nil
}

func controllerKey(blueprintID, version, digest string) string {
	return blueprintID + "\x00" + version + "\x00" + digest
}

func hasAction(actions []v1.PreviewLifecycleAction, expected v1.PreviewLifecycleAction) bool {
	for _, action := range actions {
		if action == expected {
			return true
		}
	}
	return false
}

func cloneCapability(value v1.PreviewLifecycleProfileCapability) v1.PreviewLifecycleProfileCapability {
	value.Actions = append([]v1.PreviewLifecycleAction(nil), value.Actions...)
	if value.MaxExtensionMS != nil {
		copyValue := *value.MaxExtensionMS
		value.MaxExtensionMS = &copyValue
	}
	return value
}
