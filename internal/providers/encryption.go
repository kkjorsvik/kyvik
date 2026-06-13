package providers

import (
	"encoding/base64"
	"errors"

	"github.com/kkjorsvik/kyvik/internal/secrets"
)

// Encryptor wraps secrets.Encrypt / secrets.Decrypt with the master key.
type Encryptor struct {
	key []byte
}

// NewEncryptor returns an Encryptor using the given 32-byte AES-256 master key.
// If key is nil, Encrypt/Decrypt will return ErrNoMasterKey.
func NewEncryptor(key []byte) *Encryptor {
	return &Encryptor{key: key}
}

// ErrNoMasterKey is returned when encryption is attempted without a master key.
var ErrNoMasterKey = errors.New("providers: master key not set, cannot encrypt/decrypt API keys")

// Encrypt encrypts plaintext and returns a base64-encoded ciphertext string.
func (e *Encryptor) Encrypt(plaintext string) (string, error) {
	if len(e.key) == 0 {
		return "", ErrNoMasterKey
	}
	ct, err := secrets.Encrypt(e.key, []byte(plaintext))
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(ct), nil
}

// Decrypt decodes a base64-encoded ciphertext string and decrypts it.
func (e *Encryptor) Decrypt(encoded string) (string, error) {
	if len(e.key) == 0 {
		return "", ErrNoMasterKey
	}
	ct, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", err
	}
	pt, err := secrets.Decrypt(e.key, ct)
	if err != nil {
		return "", err
	}
	return string(pt), nil
}

// HasKey reports whether a master key is configured.
func (e *Encryptor) HasKey() bool {
	return len(e.key) > 0
}
