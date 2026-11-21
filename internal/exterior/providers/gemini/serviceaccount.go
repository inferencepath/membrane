package gemini

import "github.com/inferencepath/membrane/internal/exterior/providers/gcpauth"

// WithServiceAccountKey selects Vertex AI bearer auth minted from the request's
// Egress Identity secret. Only service-account credentials are accepted.
func WithServiceAccountKey() Option {
	return func(a *Adapter) { a.saTokens = &gcpauth.ServiceAccountTokens{} }
}
