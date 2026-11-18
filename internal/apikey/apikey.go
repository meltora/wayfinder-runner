package apikey

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

const (
	MinLength = 16
	MaxLength = 4096
)

type Source struct {
	Env  string `yaml:"env,omitempty"`
	File string `yaml:"file,omitempty"`
}

func EnvironmentName(provider string) string {
	switch provider {
	case "claude":
		return "ANTHROPIC_API_KEY"
	case "codex":
		return "CODEX_API_KEY"
	default:
		return ""
	}
}

func Resolve(source Source) (string, error) {
	if (source.Env == "") == (source.File == "") {
		return "", errors.New("provider API key source must name exactly one of env or file")
	}
	if source.Env != "" {
		value, ok := os.LookupEnv(source.Env)
		if !ok {
			return "", fmt.Errorf("provider API key environment variable %s is not set", source.Env)
		}
		return checkedValue(value, "environment variable "+source.Env)
	}
	value, err := readSecretFile(source.File)
	if err != nil {
		return "", err
	}
	return checkedValue(strings.TrimRight(value, "\r\n"), "file "+source.File)
}

func readSecretFile(path string) (string, error) {
	file, err := os.OpenFile(filepath.Clean(path), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", fmt.Errorf("provider API key file %s is not readable or is a link", path)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("provider API key file %s is not readable", path)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("provider API key file %s must be a regular file, not a link or directory", path)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("provider API key file %s must not be readable by group or others", path)
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && int(stat.Uid) != os.Geteuid() {
		return "", fmt.Errorf("provider API key file %s must be owned by the runner user", path)
	}
	if info.Size() > MaxLength+2 {
		return "", fmt.Errorf("provider API key file %s is too large", path)
	}
	data, err := io.ReadAll(io.LimitReader(file, MaxLength+3))
	if err != nil || len(data) > MaxLength+2 {
		return "", fmt.Errorf("provider API key file %s is not readable", path)
	}
	return string(data), nil
}

func checkedValue(value, origin string) (string, error) {
	if len(value) < MinLength || len(value) > MaxLength {
		return "", fmt.Errorf("provider API key from %s has an invalid length", origin)
	}
	for index := 0; index < len(value); index++ {
		if value[index] < 0x21 || value[index] > 0x7e {
			return "", fmt.Errorf("provider API key from %s contains whitespace or control characters", origin)
		}
	}
	return value, nil
}
