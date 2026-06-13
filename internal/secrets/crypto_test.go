package secrets

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"testing"

	"github.com/kkjorsvik/kyvik/pkg/types"
)

func generateTestKey(t *testing.T) []byte {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	return key
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	key := generateTestKey(t)
	plaintext := []byte("super-secret-api-key-12345")

	encrypted, err := Encrypt(key, plaintext)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	decrypted, err := Decrypt(key, encrypted)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}

	if !bytes.Equal(plaintext, decrypted) {
		t.Fatalf("round-trip mismatch: got %q, want %q", decrypted, plaintext)
	}
}

func TestDecryptWrongKey(t *testing.T) {
	key1 := generateTestKey(t)
	key2 := generateTestKey(t)
	plaintext := []byte("secret")

	encrypted, err := Encrypt(key1, plaintext)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	_, err = Decrypt(key2, encrypted)
	if !errors.Is(err, types.ErrDecryptionFailed) {
		t.Fatalf("expected ErrDecryptionFailed, got %v", err)
	}
}

func TestDecryptTruncatedData(t *testing.T) {
	key := generateTestKey(t)
	plaintext := []byte("secret")

	encrypted, err := Encrypt(key, plaintext)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	// Truncate to less than nonce size
	_, err = Decrypt(key, encrypted[:5])
	if !errors.Is(err, types.ErrDecryptionFailed) {
		t.Fatalf("expected ErrDecryptionFailed, got %v", err)
	}
}

func TestEncryptProducesUniqueNonces(t *testing.T) {
	key := generateTestKey(t)
	plaintext := []byte("same-value")

	enc1, err := Encrypt(key, plaintext)
	if err != nil {
		t.Fatalf("Encrypt 1: %v", err)
	}
	enc2, err := Encrypt(key, plaintext)
	if err != nil {
		t.Fatalf("Encrypt 2: %v", err)
	}

	if bytes.Equal(enc1, enc2) {
		t.Fatal("two encryptions of the same plaintext produced identical ciphertext")
	}
}

func TestLoadMasterKeyValid(t *testing.T) {
	key := generateTestKey(t)
	encoded := base64.StdEncoding.EncodeToString(key)
	t.Setenv("KYVIK_MASTER_KEY", encoded)

	loaded, err := LoadMasterKey()
	if err != nil {
		t.Fatalf("LoadMasterKey: %v", err)
	}
	if !bytes.Equal(key, loaded) {
		t.Fatal("loaded key does not match")
	}
}

func TestLoadMasterKeyMissing(t *testing.T) {
	t.Setenv("KYVIK_MASTER_KEY", "")

	_, err := LoadMasterKey()
	if !errors.Is(err, types.ErrMasterKeyRequired) {
		t.Fatalf("expected ErrMasterKeyRequired, got %v", err)
	}
}

func TestLoadMasterKeyBadBase64(t *testing.T) {
	t.Setenv("KYVIK_MASTER_KEY", "not-valid-base64!!!")

	_, err := LoadMasterKey()
	if err == nil {
		t.Fatal("expected error for bad base64")
	}
}

func TestLoadMasterKeyWrongLength(t *testing.T) {
	// 16 bytes instead of 32
	shortKey := make([]byte, 16)
	encoded := base64.StdEncoding.EncodeToString(shortKey)
	t.Setenv("KYVIK_MASTER_KEY", encoded)

	_, err := LoadMasterKey()
	if err == nil {
		t.Fatal("expected error for wrong length key")
	}
}
