package gemini

import (
	"context"
	"crypto/sha256"
	"errors"
	"sync"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const cloudPlatformScope = "https://www.googleapis.com/auth/cloud-platform"

// errServiceAccountKey never wraps a parser error, which could quote key data.
var errServiceAccountKey = errors.New("gemini: the sealed value is not a valid service account JSON key")

// WithServiceAccountKey selects Vertex AI bearer auth minted from the request's
// Egress Identity secret. Only service-account credentials are accepted.
func WithServiceAccountKey() Option {
	return func(a *Adapter) { a.saTokens = &serviceAccountTokens{} }
}

// serviceAccountTokens reuses a token source until its key changes or the
// underlying token source refreshes near expiry.
type serviceAccountTokens struct {
	mu  sync.Mutex
	key [sha256.Size]byte
	ts  oauth2.TokenSource
}

func (s *serviceAccountTokens) token(keyJSON string) (string, error) {
	sum := sha256.Sum256([]byte(keyJSON))
	s.mu.Lock()
	if s.ts == nil || s.key != sum {
		creds, err := google.CredentialsFromJSONWithType(
			context.Background(), []byte(keyJSON), google.ServiceAccount, cloudPlatformScope,
		)
		if err != nil {
			s.mu.Unlock()
			return "", errServiceAccountKey
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
