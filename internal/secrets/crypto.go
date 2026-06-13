// Package secrets provides an encrypted secrets vault backed by PostgreSQL.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"

	"github.com/kkjorsvik/kyvik/pkg/types"
)

// LoadMasterKey reads the KYVIK_MASTER_KEY environment variable, base64-decodes
// it, and validates it is exactly 32 bytes (AES-256).
func LoadMasterKey() ([]byte, error) {
	encoded := os.Getenv("KYVIK_MASTER_KEY")
	if encoded == "" {
		return nil, types.ErrMasterKeyRequired
	}

	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("KYVIK_MASTER_KEY: invalid base64: %w", err)
	}

	if len(key) != 32 {
		return nil, fmt.Errorf("KYVIK_MASTER_KEY: expected 32 bytes, got %d", len(key))
	}

	return key, nil
}

// Encrypt encrypts plaintext using AES-256-GCM with a random nonce.
// The returned data is nonce (12 bytes) || ciphertext.
func Encrypt(key, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create GCM: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}

	ciphertext := gcm.Seal(nonce, nonce, plaintext, nil)
	return ciphertext, nil
}

// Decrypt decrypts data produced by Encrypt using AES-256-GCM.
// It expects data in the format nonce (12 bytes) || ciphertext.
func Decrypt(key, data []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, types.ErrDecryptionFailed
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, types.ErrDecryptionFailed
	}

	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return nil, types.ErrDecryptionFailed
	}

	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, types.ErrDecryptionFailed
	}

	return plaintext, nil
}
