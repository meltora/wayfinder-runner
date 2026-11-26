package claudelogin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

const (
	MaxExchangeBytes   = 1 << 20
	maxClaudeJSONBytes = 32 << 20
	credentialsFile    = ".credentials.json"
)

var ErrExchangeMessage = errors.New("the login exchange message is invalid")

type ExchangeRequest struct {
	Host        json.RawMessage `json:"host"`
	HostAccount Account         `json:"hostAccount"`
	Baseline    [32]byte        `json:"baseline"`
}

type ExchangeResult struct {
	Job        json.RawMessage `json:"job,omitempty"`
	JobAccount Account         `json:"jobAccount"`
	Replaced   bool            `json:"replaced"`
	Reason     Reason          `json:"reason"`
}

func DecodeExchangeRequest(data []byte) (ExchangeRequest, error) {
	var request ExchangeRequest
	if err := decodeExchangeMessage(data, &request); err != nil {
		return ExchangeRequest{}, err
	}
	return request, nil
}

func DecodeExchangeResult(data []byte) (ExchangeResult, error) {
	var result ExchangeResult
	if err := decodeExchangeMessage(data, &result); err != nil {
		return ExchangeResult{}, err
	}
	return result, nil
}

func decodeExchangeMessage(data []byte, target any) error {
	if len(data) > MaxExchangeBytes {
		return ErrExchangeMessage
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return ErrExchangeMessage
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return ErrExchangeMessage
	}
	return nil
}

func Exchange(ctx context.Context, home string, request ExchangeRequest, options LockOptions) (result ExchangeResult, err error) {
	host, err := ParseOAuth(request.Host)
	if err != nil {
		return ExchangeResult{}, fmt.Errorf("the host login in the exchange request is unusable: %w", err)
	}
	configDir := filepath.Join(home, ".claude")
	unlock, err := LockConfigDir(ctx, configDir, options)
	if err != nil {
		return ExchangeResult{}, err
	}
	defer func() {
		if unlockErr := unlock(); unlockErr != nil && err == nil {
			err = unlockErr
		}
	}()
	data, job, err := ReadLogin(configDir)
	if err != nil {
		return ExchangeResult{Reason: ReasonUnparsable}, nil
	}
	result = ExchangeResult{Job: job.OAuth}
	if result.JobAccount, err = ReadAccount(filepath.Join(home, ".claude.json")); err != nil {
		result.Reason = ReasonUnparsable
		return result, nil
	}
	result.Reason = Decide(host, job, request.Baseline, request.HostAccount, result.JobAccount)
	if result.Reason != ReasonOK {
		return result, nil
	}
	merged, err := MergeCredentials(data, host.OAuth)
	if err != nil {
		return ExchangeResult{}, err
	}
	if err := WriteFileAtomic(configDir, credentialsFile, merged, -1); err != nil {
		return ExchangeResult{}, fmt.Errorf("write the job login: %w", err)
	}
	result.Replaced = true
	return result, nil
}

func ParseOAuth(oauth json.RawMessage) (Login, error) {
	if checkObject(oauth) != nil {
		return Login{}, ErrUnparsable
	}
	return ParseCredentials(append(append([]byte(`{"`+oauthKey+`":`), oauth...), '}'))
}

func ReadLogin(configDir string) ([]byte, Login, error) {
	data, err := readHomeFile(configDir, credentialsFile, MaxExchangeBytes)
	if err != nil {
		return nil, Login{}, err
	}
	login, err := ParseCredentials(data)
	if err != nil {
		return nil, Login{}, err
	}
	return data, login, nil
}

func ReadAccount(stateFile string) (Account, error) {
	data, err := readHomeFile(filepath.Dir(stateFile), filepath.Base(stateFile), maxClaudeJSONBytes)
	if errors.Is(err, fs.ErrNotExist) {
		return Account{}, nil
	}
	if err != nil {
		return Account{}, err
	}
	return ParseAccount(data)
}

func readHomeFile(dir, name string, limit int64) ([]byte, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", name)
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s is larger than %d bytes", name, limit)
	}
	return data, nil
}
