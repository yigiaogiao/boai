package ai

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
)

// ErrSecretMissing reports that the process has no encryption secret.
var ErrSecretMissing = errors.New("ai: encryption secret is not configured")

const secretPrefix = "v1:"

// EncryptKey protects an API key at rest. The process secret is hashed into
// an AES-256 key and the result is authenticated with GCM.
func EncryptKey(secret, plain string) (string, error) {
	if strings.TrimSpace(secret) == "" {
		return "", ErrSecretMissing
	}
	if plain == "" {
		return "", errors.New("ai: empty API key")
	}
	block, err := aes.NewCipher(deriveKey(secret))
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nil, nonce, []byte(plain), nil)
	return secretPrefix + base64.RawStdEncoding.EncodeToString(append(nonce, sealed...)), nil
}

// DecryptKey reverses EncryptKey.
func DecryptKey(secret, encoded string) (string, error) {
	if strings.TrimSpace(secret) == "" {
		return "", ErrSecretMissing
	}
	if !strings.HasPrefix(encoded, secretPrefix) {
		return "", errors.New("ai: unsupported ciphertext")
	}
	raw, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(encoded, secretPrefix))
	if err != nil {
		return "", fmt.Errorf("ai: decode ciphertext: %w", err)
	}
	block, err := aes.NewCipher(deriveKey(secret))
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", errors.New("ai: ciphertext is too short")
	}
	nonce, sealed := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, sealed, nil)
	if err != nil {
		return "", errors.New("ai: cannot decrypt API key")
	}
	return string(plain), nil
}

func deriveKey(secret string) []byte {
	sum := sha256.Sum256([]byte(secret))
	return sum[:]
}
