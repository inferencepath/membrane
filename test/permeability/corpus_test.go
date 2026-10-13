// Package permeabilitycorpus exercises the Permeability loader and the
// published JSON Schema contract against a fixture corpus. It guards the
// Membrane's source of truth: valid specs load, and every class of unsafe or
// malformed spec is rejected.
package permeabilitycorpus

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"

	"github.com/inferencepath/membrane/internal/membrane/permeability"
)

const (
	corpusDir  = "../testdata/permeability"
	schemaPath = "../testdata/permeability.schema.json"
	schemaID   = "https://inferencepath.dev/schemas/permeability-v1.json"
)

func corpusFiles(t *testing.T, sub string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(corpusDir, sub, "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) == 0 {
		t.Fatalf("no fixtures found under %s/%s", corpusDir, sub)
	}
	return matches
}

func TestLoaderAcceptsValidCorpus(t *testing.T) {
	for _, f := range corpusFiles(t, "valid") {
		t.Run(filepath.Base(f), func(t *testing.T) {
			if _, err := permeability.Load(f); err != nil {
				t.Fatalf("valid fixture %s rejected: %v", f, err)
			}
		})
	}
}

func TestLoaderRejectsInvalidCorpus(t *testing.T) {
	for _, f := range corpusFiles(t, "invalid") {
		t.Run(filepath.Base(f), func(t *testing.T) {
			if _, err := permeability.Load(f); err == nil {
				t.Fatalf("invalid fixture %s was accepted; loader must fail closed", f)
			}
		})
	}
}

// compileSchema loads the published Permeability JSON Schema.
func compileSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	data, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource(schemaID, doc); err != nil {
		t.Fatalf("add schema resource: %v", err)
	}
	schema, err := c.Compile(schemaID)
	if err != nil {
		t.Fatalf("compile schema: %v", err)
	}
	return schema
}

// yamlFileToJSONValue reads a YAML fixture and returns a JSON-compatible value
// (map[string]any with string keys) suitable for schema validation.
func yamlFileToJSONValue(t *testing.T, path string) (any, bool) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var y any
	if err := yaml.Unmarshal(raw, &y); err != nil {
		return nil, false // not parseable as YAML; parser-level failure, not a schema concern
	}
	// Round-trip through JSON to normalise key types to strings.
	jb, err := json.Marshal(y)
	if err != nil {
		return nil, false
	}
	v, err := jsonschema.UnmarshalJSON(bytes.NewReader(jb))
	if err != nil {
		return nil, false
	}
	return v, true
}

func TestSchemaContractValidFixtures(t *testing.T) {
	schema := compileSchema(t)
	for _, f := range corpusFiles(t, "valid") {
		t.Run(filepath.Base(f), func(t *testing.T) {
			v, ok := yamlFileToJSONValue(t, f)
			if !ok {
				t.Fatalf("valid fixture %s is not parseable", f)
			}
			if err := schema.Validate(v); err != nil {
				t.Fatalf("valid fixture %s violates schema: %v", f, err)
			}
		})
	}
}

// structurallyInvalid are fixtures the JSON Schema itself must reject (as
// opposed to purely semantic rules enforced only by the loader, e.g. the
// outbound denylist or empty-but-structurally-valid allow lists).
var structurallyInvalid = map[string]bool{
	"unknown-direction.yaml":   true,
	"duplicate-keys.yaml":      true,
	"unsupported-version.yaml": true,
}

func TestSchemaContractRejectsStructurallyInvalid(t *testing.T) {
	schema := compileSchema(t)
	for name := range structurallyInvalid {
		path := filepath.Join(corpusDir, "invalid", name)
		t.Run(name, func(t *testing.T) {
			v, ok := yamlFileToJSONValue(t, path)
			if !ok {
				t.Fatalf("fixture %s should be parseable YAML", path)
			}
			if err := schema.Validate(v); err == nil {
				t.Fatalf("schema accepted structurally-invalid fixture %s", path)
			}
		})
	}
}
