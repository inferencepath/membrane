// Package secrets is the sealed secret store. Provider credential values are
// encrypted with AES-256-GCM and only ciphertext is persisted. The AEAD key is
// derived from a master key kept outside the database (env:, file: or vault:),
// so a copy of the database file alone does not reveal any secret.
//
// A stored value is referenced from configuration as secret:<name>.
// Plaintext is never written to disk or logged.
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
	// minMasterKeyLen is the minimum master key length in bytes.
	minMasterKeyLen = 16
	// dataKeyLen is the derived AES-256 key length in bytes.
	dataKeyLen = 32
	// hkdfInfo separates the data key from any other use of the master key.
	hkdfInfo = "membrane/secrets/aes-256-gcm/v1"
)

// hkdfSalt is a fixed salt. HKDF salts need not be secret; this one only
// separates this derivation from others.
var hkdfSalt = []byte("membrane-sealed-secret-store")

// Keeper seals and opens secret values with AES-256-GCM, using a key derived
// from the master key with HKDF-SHA256. The raw master key is never used as
// the cipher key. A Keeper is safe for concurrent use.
type Keeper struct {
	aead       cipher.AEAD
	keyVersion int
}

// NewKeeper derives an AES-256-GCM AEAD from masterKey. keyVersion is stored
// with each sealed value so a later master key rotation can re-wrap older
// ciphertexts. It fails closed on short key material or a version below 1.
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

// Seal encrypts plaintext under a fresh random nonce and returns the
// ciphertext and the nonce.
func (k *Keeper) Seal(plaintext []byte) (ciphertext, nonce []byte, err error) {
	nonce = make([]byte, k.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, fmt.Errorf("secrets: read nonce: %w", err)
	}
	ciphertext = k.aead.Seal(nil, nonce, plaintext, nil)
	return ciphertext, nonce, nil
}

// Open decrypts a ciphertext and nonce produced by Seal. A wrong key or
// tampered input fails GCM authentication rather than returning a wrong value.
// The error never includes the value.
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
