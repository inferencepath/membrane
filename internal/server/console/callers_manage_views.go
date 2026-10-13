package console

// This file defines the console's CallerManager port: first-class creation and
// deletion of Caller Keys. The existing ConfigEditor callers form edits only
// allowlists (hashes untouched); this port adds a caller end-to-end.
//
// Isolation holds: the console never hashes or stores the secret itself. The
// plaintext secret is passed straight through to the injected manager (the app
// layer), which hashes it to SHA-256 and persists only the hash. No method ever
// returns a secret value — responses carry status only.

// CallerInput is the create payload for one Caller Key: its public key id, the
// Interior caller id, an optional display name, the deny-by-default pathway
// allowlist, and the plaintext secret (hashed by the app layer, never stored or
// echoed).
type CallerInput struct {
	KeyID    string   `json:"keyId"`
	CallerID string   `json:"callerId"`
	Name     string   `json:"name"`
	Allow    []string `json:"allow"`
	Secret   string   `json:"secret"`
}

// CallerManager creates and deletes Caller Keys. A nil CallerManager disables
// the create/delete endpoints (503). Create hashes the secret and upserts the
// caller atomically (validate → write → reload); Delete removes it.
type CallerManager interface {
	CreateCaller(in CallerInput) ApplyResult
	DeleteCaller(keyID string) (bool, error)
}
