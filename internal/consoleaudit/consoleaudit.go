// Package consoleaudit implements console.AuditSource over the Interior-only
// usage audit log (JSON Lines), so the console package never imports Interior
// code.
//
// Audit records carry no content, secret, Reveal flag, or Exterior topology,
// only usage facts such as caller ID, pathway, outcome, and token counts.
package consoleaudit

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"

	"github.com/inferencepath/membrane/internal/interior/audit"
	"github.com/inferencepath/membrane/internal/server/console"
)

// Source reads recent usage records from a JSON Lines audit file.
type Source struct {
	path string
}

var _ console.AuditSource = (*Source)(nil)

// New returns a Source reading the audit file at path.
func New(path string) *Source {
	return &Source{path: path}
}

// Recent returns up to limit most-recent usage records, newest first. A missing
// file yields an empty result, since auditing may not have written yet.
func (s *Source) Recent(limit int) ([]console.UsageRecord, error) {
	if limit <= 0 {
		return nil, nil
	}
	f, err := os.Open(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return []console.UsageRecord{}, nil
		}
		return nil, fmt.Errorf("consoleaudit: open %s: %w", s.path, err)
	}
	defer f.Close()

	// Keep only the last limit records to bound memory on large logs.
	ring := make([]console.UsageRecord, 0, limit)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec audit.Record
		if err := json.Unmarshal(line, &rec); err != nil {
			// Skip malformed lines rather than failing the whole view.
			continue
		}
		v := console.UsageRecord{
			Time:             rec.Timestamp,
			CallerID:         rec.CallerID,
			Pathway:          string(rec.PathwayID),
			Outcome:          rec.Outcome,
			DurationMillis:   rec.DurationMillis,
			PromptTokens:     rec.PromptTokens,
			CompletionTokens: rec.CompletionTokens,
			TotalTokens:      rec.TotalTokens,
			Stream:           rec.Stream,
		}
		if len(ring) == limit {
			ring = ring[1:]
		}
		ring = append(ring, v)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("consoleaudit: read %s: %w", s.path, err)
	}

	// Reverse to newest-first.
	out := make([]console.UsageRecord, len(ring))
	for i, v := range ring {
		out[len(ring)-1-i] = v
	}
	return out, nil
}
