// Package secrets is the sealed secret store: it encrypts provider credential
// values (API keys, AWS credentials) with AES-256-GCM and persists only
// ciphertext in the embedded state store, never plaintext. The AEAD key is
// derived from an operator-provided master key that lives OUTSIDE the database
// (env:/file:/vault:), so a stolen database file alone does not reveal secrets.
//
// This package lets an operator paste a provider secret once (via the Console)
// and reference it from a credential as `secret:<name>`; applications keep
// calling Membrane by pathway only and never see a key. Plaintext is never
// written to disk or logged.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
)

const (
	// minMasterKeyLen is the minimum accepted master key material length in bytes.
	minMasterKeyLen = 16
	// dataKeyLen is the derived AES-256 key length in bytes.
	dataKeyLen = 32
	// hkdfInfo domain-separates the derived data key from any other use of the
	// same master key material.
	hkdfInfo = "membrane/secrets/aes-256-gcm/v1"
)

// hkdfSalt is a fixed, non-secret salt for HKDF. A constant salt is acceptable:
// an HKDF salt need not be secret, and here it only domain-separates this
// derivation.
var hkdfSalt = []byte("membrane-sealed-secret-store")

// Keeper seals and opens secret values with AES-256-GCM. The AEAD key is derived
// from the operator-provided master key material via HKDF-SHA256, so the raw
// master key is never used directly as the cipher key. A Keeper is safe for
// concurrent use.
type Keeper struct {
	aead       cipher.AEAD
	keyVersion int
}

// NewKeeper derives an AES-256-GCM AEAD from master key material and tags it with
// keyVersion (recorded alongside each sealed value so a future master-key
// rotation can re-wrap older ciphertexts). It fails closed on short key material
// or an invalid key version.
func NewKeeper(masterKey []byte, keyVersion int) (*Keeper, error) {
	if len(masterKey) < minMasterKeyLen {
		return nil, fmt.Errorf("secrets: master key too short (%d bytes; need >= %d)", len(masterKey), minMasterKeyLen)
	}
	if keyVersion < 1 {
		return nil, errors.New("secrets: key version must be >= 1")
	}
	dk, err := hkdf.Key(sha256.New, masterKey, hkdfSalt, hkdfInfo, dataKeyLen)
	if err != nil {
		return nil, fmt.Errorf("secrets: derive data key: %w", err)
	}
	block, err := aes.NewCipher(dk)
	if err != nil {
		return nil, fmt.Errorf("secrets: new cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secrets: new gcm: %w", err)
	}
	return &Keeper{aead: aead, keyVersion: keyVersion}, nil
}

// KeyVersion returns the key version tag recorded with values sealed by this
// Keeper.
func (k *Keeper) KeyVersion() int { return k.keyVersion }

// Seal encrypts plaintext and returns the ciphertext and the random nonce used.
// Each call uses a fresh random nonce, so sealing the same value twice yields
// distinct ciphertexts.
func (k *Keeper) Seal(plaintext []byte) (ciphertext, nonce []byte, err error) {
	nonce = make([]byte, k.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, fmt.Errorf("secrets: read nonce: %w", err)
	}
	ciphertext = k.aead.Seal(nil, nonce, plaintext, nil)
	return ciphertext, nonce, nil
}

// Open decrypts a ciphertext/nonce pair produced by Seal. A wrong key or tampered
// input fails GCM authentication, so a corrupted or mis-keyed value never yields
// a plausible-but-wrong secret. The returned error is deliberately value-free.
func (k *Keeper) Open(ciphertext, nonce []byte) ([]byte, error) {
	if len(nonce) != k.aead.NonceSize() {
		return nil, fmt.Errorf("secrets: invalid nonce size %d (want %d)", len(nonce), k.aead.NonceSize())
	}
	pt, err := k.aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, errors.New("secrets: decrypt failed (wrong master key or corrupted value)")
	}
	return pt, nil
}
