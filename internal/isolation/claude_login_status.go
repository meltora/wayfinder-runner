package isolation

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/example/app-runner/internal/claudelogin"
)

type ClaudeLoginSource string

const (
	ClaudeLoginFile     ClaudeLoginSource = "file"
	ClaudeLoginKeychain ClaudeLoginSource = "keychain"
	ClaudeLoginNone     ClaudeLoginSource = "none"
)

type ClaudeLoginStatus struct {
	Source       ClaudeLoginSource
	HasRefresh   bool
	AccountKnown bool
	ExpiresAt    time.Time
}

func HostClaudeConfigDir(environment []string) (string, error) {
	configDir, _, err := hostClaudeFiles(environment)
	return configDir, err
}

func hostClaudeFiles(environment []string) (string, string, error) {
	hostHome, err := os.UserHomeDir()
	if err != nil {
		return "", "", err
	}
	configDir, err := claudeConfigDir(environment, hostHome)
	if err != nil {
		return "", "", err
	}
	if configDir == "" {
		return filepath.Join(hostHome, ".claude"), filepath.Join(hostHome, ".claude.json"), nil
	}
	return configDir, filepath.Join(configDir, ".claude.json"), nil
}

func HostClaudeLogin(environment []string) (ClaudeLoginStatus, error) {
	configDir, stateFile, err := hostClaudeFiles(environment)
	if err != nil {
		return ClaudeLoginStatus{}, err
	}
	credentials, err := readPrivateCredential(filepath.Join(configDir, ".credentials.json"))
	if errors.Is(err, fs.ErrNotExist) {
		if hostGOOS == "darwin" {
			return ClaudeLoginStatus{Source: ClaudeLoginKeychain}, nil
		}
		return ClaudeLoginStatus{Source: ClaudeLoginNone}, nil
	}
	if err != nil {
		return ClaudeLoginStatus{}, fmt.Errorf("read the Claude login: %w", err)
	}
	login, err := claudelogin.ParseCredentials(credentials)
	if err != nil {
		return ClaudeLoginStatus{}, errors.New("the Claude login in .credentials.json is unparsable")
	}
	status := ClaudeLoginStatus{Source: ClaudeLoginFile, HasRefresh: login.HasRefresh, AccountKnown: login.Account.Known(), ExpiresAt: time.UnixMilli(login.ExpiresAt).UTC()}
	if !status.AccountKnown {
		if state, err := readPrivateCredential(stateFile); err == nil {
			if account, err := claudelogin.ParseAccount(state); err == nil {
				status.AccountKnown = account.Known()
			}
		}
	}
	return status, nil
}
