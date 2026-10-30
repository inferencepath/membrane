package console

// The CallerManager port creates and deletes Caller Keys. The console never
// hashes or stores a secret: the plaintext goes to the injected manager, which
// persists only its SHA-256 hash. No method returns a secret value.

// CallerInput is the create payload for one Caller Key. Secret is plaintext;
// the manager hashes it and it is never stored or echoed.
type CallerInput struct {
	KeyID    string   `json:"keyId"`
	CallerID string   `json:"callerId"`
	Name     string   `json:"name"`
	Allow    []string `json:"allow"`
	Secret   string   `json:"secret"`
}

// CallerManager creates and deletes Caller Keys. A nil CallerManager disables
// the create and delete endpoints (503). Create hashes the secret and upserts
// the caller atomically, then reloads; Delete removes it.
type CallerManager interface {
	CreateCaller(in CallerInput) ApplyResult
	DeleteCaller(keyID string) (bool, error)
}
