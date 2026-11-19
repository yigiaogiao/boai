package ai

import (
	"strings"
	"testing"
)

func TestNormalizeBaseURLRespectsPublicNetworkPolicy(t *testing.T) {
	if _, err := NormalizeBaseURL("http://127.0.0.1:9999/v1", false); err == nil {
		t.Fatal("loopback provider URL was accepted in production mode")
	}
	if _, err := NormalizeBaseURL("http://localhost:9999/v1", false); err == nil {
		t.Fatal("localhost provider URL was accepted in production mode")
	}
	if _, err := NormalizeBaseURL("http://127.0.0.1:9999/v1", true); err != nil {
		t.Fatalf("test-mode loopback URL rejected: %v", err)
	}
	if _, err := NormalizeBaseURL("https://api.example.com/v1?key=1", true); err == nil {
		t.Fatal("provider URL with a query string was accepted")
	}
	if _, err := NormalizeBaseURL("https://user:"+"secret"+"@api.example.com/v1", true); err == nil {
		t.Fatal("provider URL with credentials was accepted")
	}
}

func TestAPIKeyEncryptionRoundTrip(t *testing.T) {
	encrypted, err := EncryptKey("process-secret", "sk-test-value")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(encrypted, "sk-test-value") {
		t.Fatal("ciphertext contains the plaintext key")
	}
	plain, err := DecryptKey("process-secret", encrypted)
	if err != nil || plain != "sk-test-value" {
		t.Fatalf("decrypt = %q, %v", plain, err)
	}
	if _, err := DecryptKey("other-secret", encrypted); err == nil {
		t.Fatal("ciphertext decrypted with the wrong secret")
	}
	if _, err := EncryptKey("", "sk-test-value"); err == nil {
		t.Fatal("encryption accepted an empty process secret")
	}
}
