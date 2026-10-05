package agentpack

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func writeEd25519KeyFile(t *testing.T, directory string) (string, ed25519.PrivateKey, []byte) {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		t.Fatal(err)
	}
	encoded := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	path := filepath.Join(directory, "signing.pem")
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	return path, private, encoded
}

func TestFileSignerSignsWithTheStoredEd25519Key(t *testing.T) {
	path, private, _ := writeEd25519KeyFile(t, t.TempDir())
	signer, err := NewFileSigner(path)
	if err != nil {
		t.Fatal(err)
	}
	message := []byte("proof message")
	signature, err := signer.Sign(context.Background(), message)
	if err != nil {
		t.Fatal(err)
	}
	public := private.Public().(ed25519.PublicKey)
	if !ed25519.Verify(public, message, signature) {
		t.Fatal("signature does not verify")
	}
	identity, err := SignerPublicKeyIdentity(signer)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKIXPublicKey(public)
	if identity.PublicKeySPKIBase64 != base64.StdEncoding.EncodeToString(der) || len(identity.SPKISHA256) != 64 {
		t.Fatalf("identity = %+v", identity)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := signer.Sign(cancelled, message); err == nil {
		t.Fatal("cancelled context signed")
	}
}

func TestNewFileSignerRejectsUnsafeOrWrongKeysWithoutLeakingMaterial(t *testing.T) {
	ecKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	ecDER, _ := x509.MarshalPKCS8PrivateKey(ecKey)
	ecPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: ecDER})
	sec1DER, _ := x509.MarshalECPrivateKey(ecKey)
	sec1PEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: sec1DER})
	cases := map[string]func(t *testing.T, directory string) (string, []byte){
		"group readable": func(t *testing.T, directory string) (string, []byte) {
			path, _, encoded := writeEd25519KeyFile(t, directory)
			if err := os.Chmod(path, 0o640); err != nil {
				t.Fatal(err)
			}
			return path, encoded
		},
		"other readable": func(t *testing.T, directory string) (string, []byte) {
			path, _, encoded := writeEd25519KeyFile(t, directory)
			if err := os.Chmod(path, 0o604); err != nil {
				t.Fatal(err)
			}
			return path, encoded
		},
		"symlink": func(t *testing.T, directory string) (string, []byte) {
			path, _, encoded := writeEd25519KeyFile(t, directory)
			link := filepath.Join(directory, "link.pem")
			if err := os.Symlink(path, link); err != nil {
				t.Fatal(err)
			}
			return link, encoded
		},
		"directory": func(t *testing.T, directory string) (string, []byte) {
			path := filepath.Join(directory, "dir.pem")
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
			return path, nil
		},
		"fifo": func(t *testing.T, directory string) (string, []byte) {
			path := filepath.Join(directory, "fifo.pem")
			if err := syscall.Mkfifo(path, 0o600); err != nil {
				t.Fatal(err)
			}
			return path, nil
		},
		"missing": func(t *testing.T, directory string) (string, []byte) {
			return filepath.Join(directory, "missing.pem"), nil
		},
		"oversize": func(t *testing.T, directory string) (string, []byte) {
			path := filepath.Join(directory, "big.pem")
			if err := os.WriteFile(path, make([]byte, signingKeyMaxBytes+1), 0o600); err != nil {
				t.Fatal(err)
			}
			return path, nil
		},
		"not pem": func(t *testing.T, directory string) (string, []byte) {
			return writeRawKeyFile(t, directory, []byte("not a key"))
		},
		"ecdsa pkcs8": func(t *testing.T, directory string) (string, []byte) {
			return writeRawKeyFile(t, directory, ecPEM)
		},
		"sec1 block": func(t *testing.T, directory string) (string, []byte) {
			return writeRawKeyFile(t, directory, sec1PEM)
		},
		"extra block": func(t *testing.T, directory string) (string, []byte) {
			_, _, encoded := writeEd25519KeyFile(t, t.TempDir())
			return writeRawKeyFile(t, directory, append(append([]byte(nil), encoded...), encoded...))
		},
		"trailing text": func(t *testing.T, directory string) (string, []byte) {
			_, _, encoded := writeEd25519KeyFile(t, t.TempDir())
			return writeRawKeyFile(t, directory, append(append([]byte(nil), encoded...), []byte("extra")...))
		},
		"leading text": func(t *testing.T, directory string) (string, []byte) {
			_, _, encoded := writeEd25519KeyFile(t, t.TempDir())
			return writeRawKeyFile(t, directory, append([]byte("note\n"), encoded...))
		},
		"encrypted headers": func(t *testing.T, directory string) (string, []byte) {
			_, private, _ := writeEd25519KeyFile(t, t.TempDir())
			der, _ := x509.MarshalPKCS8PrivateKey(private)
			return writeRawKeyFile(t, directory, pem.EncodeToMemory(&pem.Block{
				Type: "PRIVATE KEY", Headers: map[string]string{"Proc-Type": "4,ENCRYPTED", "DEK-Info": "AES-256-CBC,00"}, Bytes: der,
			}))
		},
		"encrypted pkcs8": func(t *testing.T, directory string) (string, []byte) {
			return writeRawKeyFile(t, directory, pem.EncodeToMemory(&pem.Block{Type: "ENCRYPTED PRIVATE KEY", Bytes: []byte{0x30, 0x00}}))
		},
		"garbage der": func(t *testing.T, directory string) (string, []byte) {
			return writeRawKeyFile(t, directory, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("garbage")}))
		},
	}
	for name, prepare := range cases {
		t.Run(name, func(t *testing.T) {
			directory := t.TempDir()
			path, material := prepare(t, directory)
			_, err := NewFileSigner(path)
			if err == nil {
				t.Fatal("unsafe key accepted")
			}
			message := err.Error()
			if !strings.Contains(message, path) {
				t.Fatalf("error %q does not name the path", message)
			}
			assertNoKeyMaterial(t, message, material)
		})
	}
}

func assertNoKeyMaterial(t *testing.T, output string, material []byte) {
	t.Helper()
	if strings.Contains(output, "-----BEGIN") || strings.Contains(output, "PRIVATE KEY-----") {
		t.Fatalf("output contains PEM framing: %q", output)
	}
	if len(material) == 0 {
		return
	}
	block, _ := pem.Decode(material)
	if block == nil {
		return
	}
	body := base64.StdEncoding.EncodeToString(block.Bytes)
	for start := 0; start+16 <= len(body); start += 16 {
		if strings.Contains(output, body[start:start+16]) {
			t.Fatalf("output contains key material")
		}
	}
	if strings.Contains(output, hex.EncodeToString(block.Bytes)) {
		t.Fatalf("output contains hex key material")
	}
}

func writeRawKeyFile(t *testing.T, directory string, contents []byte) (string, []byte) {
	t.Helper()
	path := filepath.Join(directory, "key.pem")
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	return path, contents
}

func TestGenerateFileSigningKeyWritesOwnerOnlyKeyOnceAtomically(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "keys", "nested")
	path := filepath.Join(parent, "platform.pem")
	signer, err := GenerateFileSigningKey(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("key mode = %v err = %v", info, err)
	}
	parentInfo, err := os.Stat(parent)
	if err != nil || parentInfo.Mode().Perm() != 0o700 {
		t.Fatalf("parent mode = %v err = %v", parentInfo, err)
	}
	reloaded, err := NewFileSigner(path)
	if err != nil {
		t.Fatal(err)
	}
	first, _ := signer.PublicKeySPKI()
	second, _ := reloaded.PublicKeySPKI()
	if string(first) != string(second) {
		t.Fatal("reloaded key differs")
	}
	before, _ := os.ReadFile(path)
	if _, err := GenerateFileSigningKey(path); err == nil || !strings.Contains(err.Error(), "never overwritten") {
		t.Fatalf("overwrite err = %v", err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("existing key changed")
	}
	entries, _ := os.ReadDir(parent)
	if len(entries) != 1 {
		t.Fatalf("temporary files left behind: %d entries", len(entries))
	}
	if _, err := GenerateFileSigningKey(""); err == nil {
		t.Fatal("empty path accepted")
	}
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := GenerateFileSigningKey(filepath.Join(blocker, "key.pem")); err == nil {
		t.Fatal("key under a file accepted")
	}
	var empty *FileSigner
	if _, err := empty.Sign(context.Background(), nil); err == nil {
		t.Fatal("nil signer signed")
	}
	if _, err := empty.PublicKeySPKI(); err == nil {
		t.Fatal("nil signer exported a key")
	}
}

func TestNewFileSignerAcceptsSurroundingWhitespaceOnly(t *testing.T) {
	_, _, encoded := writeEd25519KeyFile(t, t.TempDir())
	path, _ := writeRawKeyFile(t, t.TempDir(), append(append([]byte("\n \r\n"), encoded...), []byte("\n\n")...))
	if _, err := NewFileSigner(path); err != nil {
		t.Fatalf("whitespace-framed key refused: %v", err)
	}
}

func TestGenerateFileSigningKeyRefusesASharedParentDirectory(t *testing.T) {
	for _, mode := range []os.FileMode{0o750, 0o705, 0o770, 0o755} {
		parent := filepath.Join(t.TempDir(), "keys")
		if err := os.Mkdir(parent, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(parent, mode); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(parent, "platform.pem")
		if _, err := GenerateFileSigningKey(path); err == nil || !strings.Contains(err.Error(), "owner-only directory") {
			t.Fatalf("mode %o err = %v", mode, err)
		}
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("mode %o wrote a key: %v", mode, err)
		}
	}
	target := filepath.Join(t.TempDir(), "real")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := GenerateFileSigningKey(filepath.Join(link, "platform.pem")); err == nil {
		t.Fatal("key under a symlinked parent accepted")
	}
}
