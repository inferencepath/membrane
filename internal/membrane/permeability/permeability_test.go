package permeability

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDenylistAccessor(t *testing.T) {
	dl := Denylist()
	if len(dl) != len(denylist) {
		t.Fatalf("Denylist() len = %d, want %d", len(dl), len(denylist))
	}
	// Sorted and complete.
	for i := 1; i < len(dl); i++ {
		if dl[i-1] > dl[i] {
			t.Fatalf("Denylist() not sorted: %v", dl)
		}
	}
	for name := range denylist {
		if _, ok := indexOf(dl, name); !ok {
			t.Fatalf("Denylist() missing %q", name)
		}
	}
	// The returned slice is a copy: mutating it must not affect the denylist.
	dl[0] = "mutated"
	if _, ok := denylist["mutated"]; ok {
		t.Fatal("Denylist() leaked a mutable reference into the enforced denylist")
	}
}

func TestParseToolPolicy(t *testing.T) {
	spec, err := Parse([]byte(`
version: 1
pathways:
  chat:
    outbound:
      allow: [messages, tools, tool_choice]
    inbound:
      default: [message]
    tool_policy:
      tools_allow: [get_weather, search]
      tools_deny: [shell_exec]
      max_tools: 8
      max_tool_result_bytes: 1024
      redact: ['(?i)ssn:\s*\d{3}-\d{2}-\d{4}']
      destinations: [internal-llm]
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	p := spec.ToolPolicy("chat")
	if p.MaxTools != 8 || p.MaxToolResultBytes != 1024 {
		t.Errorf("caps = %+v", p)
	}
	if !p.ToolAllowed("get_weather") || p.ToolAllowed("other") || p.ToolAllowed("shell_exec") {
		t.Errorf("allow/deny logic wrong: %+v", p)
	}
	if !p.DestinationAllowed("internal-llm") || p.DestinationAllowed("openai") {
		t.Errorf("destination logic wrong: %+v", p)
	}
	if got := p.RedactResult("user ssn: 123-45-6789 end"); got != "user "+RedactionPlaceholder+" end" {
		t.Errorf("redaction = %q", got)
	}
}

func TestParseToolPolicyAbsentIsPermissive(t *testing.T) {
	spec, err := Parse([]byte("version: 1\npathways:\n  chat:\n    outbound:\n      allow: [messages]\n    inbound:\n      default: [message]\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	p := spec.ToolPolicy("chat")
	if !p.ToolAllowed("anything") || !p.DestinationAllowed("any") || p.MaxTools != 0 {
		t.Errorf("zero policy must be permissive: %+v", p)
	}
	if got := p.RedactResult("untouched"); got != "untouched" {
		t.Errorf("redaction with no patterns changed content: %q", got)
	}
}

func TestParseToolPolicyRejectsBadRegexAndNegativeCaps(t *testing.T) {
	cases := []string{
		"version: 1\npathways:\n  chat:\n    outbound:\n      allow: [messages]\n    inbound:\n      default: [message]\n    tool_policy:\n      redact: ['(']\n",
		"version: 1\npathways:\n  chat:\n    outbound:\n      allow: [messages]\n    inbound:\n      default: [message]\n    tool_policy:\n      max_tools: -1\n",
		"version: 1\npathways:\n  chat:\n    outbound:\n      allow: [messages]\n    inbound:\n      default: [message]\n    tool_policy:\n      max_tool_result_bytes: -5\n",
	}
	for i, y := range cases {
		if _, err := Parse([]byte(y)); err == nil {
			t.Errorf("case %d: expected parse error, got nil", i)
		}
	}
}

func indexOf(ss []string, want string) (int, bool) {
	for i, s := range ss {
		if s == want {
			return i, true
		}
	}
	return 0, false
}

const validSpec = `
version: 1
pathways:
  chat:
    outbound:
      allow: [messages, temperature, stream]
    inbound:
      default: [message, finish_reason, usage]
      on_reveal: [provider, model]
`

func TestParseValid(t *testing.T) {
	spec, err := Parse([]byte(validSpec))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spec.Version != SupportedVersion {
		t.Fatalf("version = %d, want %d", spec.Version, SupportedVersion)
	}
	ds, ok := spec.Pathways["chat"]
	if !ok {
		t.Fatalf("missing chat pathway")
	}
	if got, want := strings.Join(ds.Outbound.Allow, ","), "messages,temperature,stream"; got != want {
		t.Fatalf("outbound.allow = %q, want %q", got, want)
	}
	if len(ds.Inbound.Default) != 3 || len(ds.Inbound.OnReveal) != 2 {
		t.Fatalf("inbound sets wrong: %+v", ds.Inbound)
	}
}

func TestParseInvalid(t *testing.T) {
	cases := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name:    "unsupported version",
			yaml:    "version: 2\npathways:\n  chat:\n    outbound:\n      allow: [messages]\n    inbound:\n      default: [message]\n",
			wantErr: "unsupported version",
		},
		{
			name:    "no pathways",
			yaml:    "version: 1\npathways: {}\n",
			wantErr: "at least one pathway",
		},
		{
			name:    "empty outbound",
			yaml:    "version: 1\npathways:\n  chat:\n    outbound:\n      allow: []\n    inbound:\n      default: [message]\n",
			wantErr: "outbound.allow must not be empty",
		},
		{
			name:    "denylisted outbound field",
			yaml:    "version: 1\npathways:\n  chat:\n    outbound:\n      allow: [messages, user]\n    inbound:\n      default: [message]\n",
			wantErr: `must not contain identity/control field "user"`,
		},
		{
			name:    "reveal is denylisted outbound",
			yaml:    "version: 1\npathways:\n  chat:\n    outbound:\n      allow: [messages, reveal]\n    inbound:\n      default: [message]\n",
			wantErr: `must not contain identity/control field "reveal"`,
		},
		{
			name:    "invalid field name",
			yaml:    "version: 1\npathways:\n  chat:\n    outbound:\n      allow: [Messages]\n    inbound:\n      default: [message]\n",
			wantErr: "invalid field name",
		},
		{
			name:    "duplicate field",
			yaml:    "version: 1\npathways:\n  chat:\n    outbound:\n      allow: [messages, messages]\n    inbound:\n      default: [message]\n",
			wantErr: "duplicate field",
		},
		{
			name:    "missing inbound default",
			yaml:    "version: 1\npathways:\n  chat:\n    outbound:\n      allow: [messages]\n    inbound:\n      on_reveal: [provider]\n",
			wantErr: "inbound.default must not be empty",
		},
		{
			name:    "unknown top-level field",
			yaml:    "version: 1\npathways:\n  chat:\n    outbound:\n      allow: [messages]\n    inbound:\n      default: [message]\nbogus: true\n",
			wantErr: "parse",
		},
		{
			name:    "unknown direction field",
			yaml:    "version: 1\npathways:\n  chat:\n    sideways:\n      allow: [messages]\n    inbound:\n      default: [message]\n",
			wantErr: "parse",
		},
		{
			name:    "malformed yaml",
			yaml:    "version: 1\n  : :\n",
			wantErr: "parse",
		},
		{
			name:    "invalid inbound field name",
			yaml:    "version: 1\npathways:\n  chat:\n    outbound:\n      allow: [messages]\n    inbound:\n      default: [Message]\n",
			wantErr: "invalid field name",
		},
		{
			name:    "invalid on_reveal field name",
			yaml:    "version: 1\npathways:\n  chat:\n    outbound:\n      allow: [messages]\n    inbound:\n      default: [message]\n      on_reveal: [Provider]\n",
			wantErr: "invalid field name",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.yaml))
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "permeability.yaml")
	if err := os.WriteFile(path, []byte(validSpec), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err != nil {
		t.Fatalf("Load valid: %v", err)
	}

	if _, err := Load(filepath.Join(dir, "does-not-exist.yaml")); err == nil {
		t.Fatalf("expected read error for missing file")
	} else if !strings.Contains(err.Error(), "read") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "read")
	}
}
