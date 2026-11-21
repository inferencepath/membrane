// Package gcpauth holds the Google Cloud pieces shared by the adapters that can
// reach Vertex AI: the regional host and service account bearer minting.
package gcpauth

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// cloudPlatformScope is the OAuth scope Vertex AI requires.
const cloudPlatformScope = "https://www.googleapis.com/auth/cloud-platform"

// ErrServiceAccountKey never wraps the parser's error, which may quote the key.
var ErrServiceAccountKey = errors.New("the sealed value is not a valid service account JSON key")

// VertexHost returns the Vertex AI host for a location. The global location
// (where many newer models are served) has no regional host prefix.
func VertexHost(location string) string {
	if location == "global" {
		return "https://aiplatform.googleapis.com"
	}
	return fmt.Sprintf("https://%s-aiplatform.googleapis.com", location)
}

// ServiceAccountTokens mints bearers from a service account JSON key and caches
// one token source per key, so tokens are reused until near expiry and a
// rotated key takes effect on the next request. Only the "service_account"
// credential type is accepted: other Google credential types (external_account
// and friends) can make the token exchange call arbitrary URLs or run local
// programs.
type ServiceAccountTokens struct {
	mu  sync.Mutex
	key [sha256.Size]byte
	ts  oauth2.TokenSource
}

// Token returns a bearer for the given service account JSON key.
func (s *ServiceAccountTokens) Token(keyJSON string) (string, error) {
	sum := sha256.Sum256([]byte(keyJSON))
	s.mu.Lock()
	if s.ts == nil || s.key != sum {
		creds, err := google.CredentialsFromJSONWithType(context.Background(), []byte(keyJSON), google.ServiceAccount, cloudPlatformScope)
		if err != nil {
			s.mu.Unlock()
			return "", ErrServiceAccountKey
		}
		s.ts, s.key = oauth2.ReuseTokenSource(nil, creds.TokenSource), sum
	}
	ts := s.ts
	s.mu.Unlock()
	tok, err := ts.Token()
	if err != nil {
		return "", err
	}
	return tok.AccessToken, nil
}
