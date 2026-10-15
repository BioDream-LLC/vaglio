// Package seal encrypts what Vaglio keeps about patients on disk: the audit log's records, the request Bundles and
// documents, and the fax inbox. AES-256-GCM under one data key the deployment holds outside the data directory (a
// secret store, a mounted file), given as VAGLIO_DATA_KEY: 32 bytes, base64 or hex.
//
// Reading takes both sealed and plain data, so encryption is switched on for a deployment that has data already: new
// writes are sealed, and `vaglio data seal` seals what was written before. A sealed file met without the key is an
// error that says so, never garbage.
//
// What is not sealed, because it holds no patient data: accounts (password hashes and roles), policies and their
// kept versions, drafts, the TLS keys (in DATA/tls, protected by file mode), cursors.
//
// The audit log's hash chain is over the plain records, so sealing a log changes no hash and no receipt; a sealed
// record is still checked against its chain when it is read.
package seal

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
)

// Key is a data key.
type Key struct{ aead cipher.AEAD }

// EnvKey is the environment variable the data key is read from.
const EnvKey = "VAGLIO_DATA_KEY"

// Parse reads a key: 32 bytes as base64 (standard or URL, padded or not) or as 64 hex digits.
func Parse(s string) (*Key, error) {
	s = strings.TrimSpace(s)
	var raw []byte
	if len(s) == 64 {
		raw, _ = hex.DecodeString(s)
	}
	if raw == nil {
		for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
			if b, err := enc.DecodeString(s); err == nil {
				raw = b
				break
			}
		}
	}
	if len(raw) != 32 {
		return nil, errors.New("the data key is 32 bytes, as base64 or 64 hex digits (openssl rand -base64 32)")
	}
	block, _ := aes.NewCipher(raw)
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Key{aead: aead}, nil
}

var (
	mu      sync.RWMutex
	current *Key
)

// SetDefault sets the key every package seals with (nil: none, write plain).
func SetDefault(k *Key) {
	mu.Lock()
	current = k
	mu.Unlock()
}

// FromEnv sets the default key from VAGLIO_DATA_KEY, if it is set; it reports whether a key is now in use.
func FromEnv() (bool, error) {
	v := os.Getenv(EnvKey)
	if v == "" {
		return false, nil
	}
	k, err := Parse(v)
	if err != nil {
		return false, fmt.Errorf("%s: %w", EnvKey, err)
	}
	SetDefault(k)
	return true, nil
}

// On is whether data is being sealed.
func On() bool {
	mu.RLock()
	defer mu.RUnlock()
	return current != nil
}

func key() *Key {
	mu.RLock()
	defer mu.RUnlock()
	return current
}

// magic begins every sealed blob; linePrefix every sealed log line.
var (
	magic      = []byte("VGSEAL1\x00")
	linePrefix = []byte("#VGSEAL1 ")
)

// ErrNoKey is sealed data met without a key.
var ErrNoKey = errors.New("this data is encrypted: set " + EnvKey + " to the deployment's data key")

func (k *Key) seal(purpose string, plain []byte) []byte {
	nonce := make([]byte, k.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		panic(err) // the system's random source failed: nothing can be sealed safely
	}
	return k.aead.Seal(nonce, nonce, plain, []byte("vaglio/"+purpose))
}

func (k *Key) open(purpose string, data []byte) ([]byte, error) {
	n := k.aead.NonceSize()
	if len(data) < n {
		return nil, errors.New("sealed data is too short")
	}
	out, err := k.aead.Open(nil, data[:n], data[n:], []byte("vaglio/"+purpose))
	if err != nil {
		return nil, errors.New("sealed data does not open with this key: the wrong key, or the data was changed")
	}
	return out, nil
}

// Blob seals a file's contents with the default key, for purpose ("bundle", "fax"); plain when no key is set.
func Blob(purpose string, plain []byte) []byte {
	k := key()
	if k == nil {
		return plain
	}
	return append(append([]byte(nil), magic...), k.seal(purpose, plain)...)
}

// Sealed is whether a blob is sealed.
func Sealed(data []byte) bool { return bytes.HasPrefix(data, magic) }

// Unblob opens a blob sealed for purpose, or returns plain data as it is.
func Unblob(purpose string, data []byte) ([]byte, error) {
	if !Sealed(data) {
		return data, nil
	}
	k := key()
	if k == nil {
		return nil, ErrNoKey
	}
	return k.open(purpose, data[len(magic):])
}

// Line seals one line of the audit log (without its newline); plain when no key is set.
func Line(plain []byte) []byte {
	k := key()
	if k == nil {
		return plain
	}
	enc := base64.StdEncoding.EncodeToString(k.seal("audit", plain))
	return append(append([]byte(nil), linePrefix...), enc...)
}

// SealedLine is whether a log line is sealed.
func SealedLine(line []byte) bool { return bytes.HasPrefix(line, linePrefix) }

// Unline opens a sealed log line, or returns a plain one as it is.
func Unline(line []byte) ([]byte, error) {
	if !SealedLine(line) {
		return line, nil
	}
	k := key()
	if k == nil {
		return nil, ErrNoKey
	}
	raw, err := base64.StdEncoding.DecodeString(string(line[len(linePrefix):]))
	if err != nil {
		return nil, errors.New("a sealed record is not base64")
	}
	return k.open("audit", raw)
}

// WriteFile writes a file sealed for purpose.
func WriteFile(path, purpose string, data []byte, perm os.FileMode) error {
	return os.WriteFile(path, Blob(purpose, data), perm)
}

// WriteFileSync writes a file sealed for purpose and syncs it to disk before returning.
func WriteFileSync(path, purpose string, data []byte, perm os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	_, err = f.Write(Blob(purpose, data))
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// ReadFile reads a file, opening it if sealed.
func ReadFile(path, purpose string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out, err := Unblob(purpose, raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return out, nil
}
