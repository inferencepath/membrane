package secrets_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/inferencepath/membrane/internal/secrets"
)

func TestSealOpenRoundTrip(t *testing.T) {
	k, err := secrets.NewKeeper([]byte("a-sufficiently-long-master-key!!"), 1)
	if err != nil {
		t.Fatalf("new keeper: %v", err)
	}
	plaintext := []byte("sk-super-secret-api-key")
	ct, nonce, err := k.Seal(plaintext)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if bytes.Contains(ct, plaintext) {
		t.Fatal("ciphertext must not contain the plaintext")
	}
	got, err := k.Open(ct, nonce)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("round trip = %q, want %q", got, plaintext)
	}
}

func TestSealUsesFreshNonce(t *testing.T) {
	k, _ := secrets.NewKeeper([]byte("a-sufficiently-long-master-key!!"), 1)
	ct1, n1, _ := k.Seal([]byte("same-value"))
	ct2, n2, _ := k.Seal([]byte("same-value"))
	if bytes.Equal(n1, n2) {
		t.Fatal("nonces must differ across seals")
	}
	if bytes.Equal(ct1, ct2) {
		t.Fatal("ciphertexts must differ across seals of the same value")
	}
}

func TestOpenWrongKeyFails(t *testing.T) {
	k1, _ := secrets.NewKeeper([]byte("master-key-number-one-padding!!!"), 1)
	k2, _ := secrets.NewKeeper([]byte("master-key-number-two-padding!!!"), 1)
	ct, nonce, _ := k1.Seal([]byte("value"))
	if _, err := k2.Open(ct, nonce); err == nil {
		t.Fatal("expected open with wrong key to fail")
	}
}

func TestOpenTamperedCiphertextFails(t *testing.T) {
	k, _ := secrets.NewKeeper([]byte("a-sufficiently-long-master-key!!"), 1)
	ct, nonce, _ := k.Seal([]byte("value"))
	ct[0] ^= 0xFF // flip a bit
	if _, err := k.Open(ct, nonce); err == nil {
		t.Fatal("expected open of tampered ciphertext to fail (GCM auth)")
	}
}

func TestNewKeeperRejectsShortKey(t *testing.T) {
	if _, err := secrets.NewKeeper([]byte("short"), 1); err == nil {
		t.Fatal("expected error for short master key")
	}
}

func TestNewKeeperRejectsBadVersion(t *testing.T) {
	if _, err := secrets.NewKeeper([]byte("a-sufficiently-long-master-key!!"), 0); err == nil {
		t.Fatal("expected error for key version < 1")
	}
}

func TestKeyVersionRecorded(t *testing.T) {
	k, _ := secrets.NewKeeper([]byte("a-sufficiently-long-master-key!!"), 3)
	if k.KeyVersion() != 3 {
		t.Fatalf("KeyVersion() = %d, want 3", k.KeyVersion())
	}
}

func TestErrorsDoNotLeakValue(t *testing.T) {
	k, _ := secrets.NewKeeper([]byte("a-sufficiently-long-master-key!!"), 1)
	ct, nonce, _ := k.Seal([]byte("top-secret-value"))
	// Wrong nonce size path and auth-failure path must not echo the value.
	if _, err := k.Open(ct, nonce[:4]); err == nil || strings.Contains(err.Error(), "top-secret") {
		t.Fatalf("unexpected error (or value leak): %v", err)
	}
}
