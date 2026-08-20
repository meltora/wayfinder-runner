package sourcepublication

import (
	"fmt"
	"strings"

	"github.com/example/app-runner/internal/safeio"
)

const maxTokenBytes = 8 << 10

func readToken(path string) (string, error) {
	raw, err := safeio.ReadRegularFile(path, maxTokenBytes, 0o077)
	if err != nil {
		return "", fmt.Errorf("read private source publication credential: %w", err)
	}
	token := strings.TrimSuffix(strings.TrimSuffix(string(raw), "\n"), "\r")
	if len(token) < 8 || len(token) > maxTokenBytes || !isPrintableASCII(token) {
		return "", fmt.Errorf("source publication credential has an invalid format")
	}
	return token, nil
}

func isPrintableASCII(value string) bool {
	for _, character := range []byte(value) {
		if character < 0x21 || character > 0x7e {
			return false
		}
	}
	return true
}
