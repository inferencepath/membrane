package pipeline

import (
	"errors"
	"fmt"

	"github.com/inferencepath/membrane/internal/types"
)

// ErrTurnCountChanged is returned when a stage grew or shrank the message slice
// it was given, so sanitized text can no longer be mapped back to its fields.
var ErrTurnCountChanged = errors.New("content stage changed the number of turns")

// TextsToMessages wraps each text field of a non-chat request as a message
// with role, so the content stage can sanitize it like a chat turn.
func TextsToMessages(role string, texts []string) []types.Message {
	out := make([]types.Message, len(texts))
	for i, t := range texts {
		out[i] = types.Message{Role: role, Content: t}
	}
	return out
}

// MessagesToTexts writes the (sanitized) message contents back into dst, in
// order. It fails if the counts differ.
func MessagesToTexts(msgs []types.Message, dst []string) error {
	if len(msgs) != len(dst) {
		return ErrTurnCountChanged
	}
	for i, m := range msgs {
		dst[i] = m.Content
	}
	return nil
}

// MediaDescriptor is the content-free stand-in recorded for an image or audio
// part: its kind, media type and size (and duration when known). It never
// takes the payload, so no media bytes can reach a trace through it.
func MediaDescriptor(kind, mediaType string, sizeBytes int, seconds float64) string {
	if mediaType == "" {
		mediaType = "unknown"
	}
	s := fmt.Sprintf("[%s: %s, %s", kind, mediaType, humanSize(sizeBytes))
	if seconds > 0 {
		s += fmt.Sprintf(", %.1f s", seconds)
	}
	return s + "]"
}

func humanSize(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%d KB", n>>10)
	default:
		return fmt.Sprintf("%d B", n)
	}
}
