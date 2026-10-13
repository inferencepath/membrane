// Package architecture holds tests that enforce cross-zone import rules: the
// Exterior and the Interior audit subsystem must not share references, so
// identity has no path across the Membrane.
package architecture

import (
	"go/build"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const module = "github.com/inferencepath/membrane"

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// test/architecture -> repo root is two levels up.
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}

// importsOfTree returns, for each buildable package dir under root/rel, its
// (non-test) import paths.
func importsOfTree(t *testing.T, root, rel string) map[string][]string {
	t.Helper()
	base := filepath.Join(root, rel)
	result := map[string][]string{}
	err := filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		pkg, perr := build.ImportDir(path, 0)
		if perr != nil {
			return nil // no buildable Go files here
		}
		result[path] = pkg.Imports
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result) == 0 {
		t.Fatalf("no packages found under %s", base)
	}
	return result
}

func TestExteriorDoesNotImportInterior(t *testing.T) {
	root := repoRoot(t)
	for dir, imps := range importsOfTree(t, root, "internal/exterior") {
		for _, imp := range imps {
			if strings.HasPrefix(imp, module+"/internal/interior") {
				t.Errorf("%s imports Interior package %s (zone violation)", dir, imp)
			}
		}
	}
}

func TestAuditDoesNotImportExterior(t *testing.T) {
	root := repoRoot(t)
	for dir, imps := range importsOfTree(t, root, "internal/interior/audit") {
		for _, imp := range imps {
			if strings.HasPrefix(imp, module+"/internal/exterior") {
				t.Errorf("%s imports Exterior package %s (zone violation)", dir, imp)
			}
		}
	}
}

// TestConsoleDoesNotImportEgressOrInterior asserts the Membrane Console is a pure
// operator surface: it must not import the egress chat handler (so it can never
// mount or build the /v1/chat path) nor any Interior/Membrane/Exterior package
// that carries caller identity, secrets, or the Scrub/Transporter contract. The
// console reads only via injected, identity-free interfaces (status + metrics).
func TestConsoleDoesNotImportEgressOrInterior(t *testing.T) {
	root := repoRoot(t)
	forbidden := []string{
		module + "/internal/server/chat",
		module + "/internal/interior",
		module + "/internal/membrane",
		module + "/internal/exterior",
	}
	for dir, imps := range importsOfTree(t, root, "internal/server/console") {
		for _, imp := range imps {
			for _, f := range forbidden {
				if strings.HasPrefix(imp, f) {
					t.Errorf("%s imports %s (console must stay off the egress path)", dir, imp)
				}
			}
		}
	}
}
