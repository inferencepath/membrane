package permeability

import (
	"strings"
	"testing"

	"github.com/inferencepath/membrane/internal/types"
)

func ds(outbound, def, reveal []string) DirectionSets {
	return DirectionSets{
		Outbound: Outbound{Allow: outbound},
		Inbound:  Inbound{Default: def, OnReveal: reveal},
	}
}

func TestValidateVocabularyChatUnrestricted(t *testing.T) {
	// chat accepts any field; the denylist is enforced by Parse.
	err := ValidateVocabulary("chat", types.OperationChat,
		ds([]string{"messages", "seed", "response_format", "frequency_penalty"},
			[]string{"message", "usage"}, []string{"provider"}))
	if err != nil {
		t.Fatalf("chat should be unrestricted, got %v", err)
	}
	// Empty operation is treated as chat.
	if err := ValidateVocabulary("chat", "", ds([]string{"anything"}, []string{"message"}, nil)); err != nil {
		t.Fatalf("empty operation (=chat) should be unrestricted, got %v", err)
	}
}

func TestValidateVocabularyEmbeddingsAccepts(t *testing.T) {
	err := ValidateVocabulary("embed", types.OperationEmbeddings,
		ds([]string{"input", "encoding_format", "dimensions"},
			[]string{"data", "usage"}, []string{"provider", "model"}))
	if err != nil {
		t.Fatalf("valid embeddings policy rejected: %v", err)
	}
}

func TestValidateVocabularyRejectsOutOfVocab(t *testing.T) {
	cases := []struct {
		name string
		op   types.Operation
		ds   DirectionSets
		want string
	}{
		{"chat field on embeddings", types.OperationEmbeddings,
			ds([]string{"messages"}, []string{"data"}, nil), `outbound.allow field "messages"`},
		{"bad inbound default", types.OperationEmbeddings,
			ds([]string{"input"}, []string{"message"}, nil), `inbound.default field "message"`},
		{"bad on_reveal", types.OperationRerank,
			ds([]string{"query", "documents"}, []string{"results"}, []string{"temperature"}), `inbound.on_reveal field "temperature"`},
		{"unknown operation", types.Operation("teleport"),
			ds([]string{"x"}, []string{"y"}, nil), `unknown operation`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateVocabulary("p", tc.op, tc.ds)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %q, want substring %q", err, tc.want)
			}
		})
	}
}

func TestVocabularyDisplay(t *testing.T) {
	out, in := Vocabulary(types.OperationEmbeddings)
	if len(out) == 0 || len(in) == 0 {
		t.Fatalf("embeddings vocabulary empty: out=%v in=%v", out, in)
	}
	// Sorted.
	for i := 1; i < len(out); i++ {
		if out[i-1] > out[i] {
			t.Fatalf("outbound vocab not sorted: %v", out)
		}
	}
	// chat is unrestricted, so it has no display vocabulary.
	co, ci := Vocabulary(types.OperationChat)
	if co != nil || ci != nil {
		t.Fatalf("chat display vocab should be nil (unrestricted), got out=%v in=%v", co, ci)
	}
}
