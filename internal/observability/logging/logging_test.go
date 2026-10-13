package logging_test

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/inferencepath/membrane/internal/observability/logging"
)

func TestRedactsSensitiveKeys(t *testing.T) {
	var buf bytes.Buffer
	logger := logging.NewWithWriter(&buf, slog.LevelInfo)

	logger.Info("test",
		"authorization", "Bearer sk-super-secret",
		"secret_ref", "env:OPENAI_KEY",
		"pathway", "chat",
	)

	out := buf.String()
	if strings.Contains(out, "sk-super-secret") {
		t.Errorf("log leaked a secret value: %s", out)
	}
	if strings.Contains(out, "env:OPENAI_KEY") {
		t.Errorf("log leaked a secret reference value: %s", out)
	}
	if !strings.Contains(out, logging.Redacted) {
		t.Errorf("expected %q in output: %s", logging.Redacted, out)
	}
	if !strings.Contains(out, "chat") {
		t.Errorf("non-sensitive field should be present: %s", out)
	}
}

func TestNew(t *testing.T) {
	if logging.New(slog.LevelDebug) == nil {
		t.Fatal("New returned nil")
	}
}
