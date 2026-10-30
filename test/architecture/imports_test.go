// Package architecture holds tests for cross-zone import rules. The Exterior
// and the Interior audit package must not import each other, so identity has
// no path across the Membrane.
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
	// The repo root is two levels above test/architecture.
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}

// importsOfTree maps each buildable package directory under root/rel to its
// non-test imports.
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

// TestConsoleDoesNotImportEgressOrInterior checks that the Membrane Console
// imports neither the egress chat handler nor any Interior, Membrane, or
// Exterior package. The Console reads status and metrics through injected,
// identity-free interfaces.
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

// allowedThirdParty lists the third-party module prefixes that cmd/ and
// internal/ may import. Anything else is a test failure.
var allowedThirdParty = []string{
	"github.com/aws/aws-sdk-go-v2",
	"github.com/aws/smithy-go",
	"github.com/Azure/azure-sdk-for-go/sdk",
	"golang.org/x/oauth2",
	"gopkg.in/yaml.v3",
	"modernc.org/sqlite",
}

// TestCoreDependencyAllowlist checks that every non-test import in cmd/ and
// internal/ is the standard library, this module, or an allowlisted module.
func TestCoreDependencyAllowlist(t *testing.T) {
	root := repoRoot(t)
	for _, rel := range []string{"internal", "cmd"} {
		for dir, imps := range importsOfTree(t, root, rel) {
			for _, imp := range imps {
				if isStdlib(imp) || imp == module || strings.HasPrefix(imp, module+"/") {
					continue
				}
				if !hasAllowedPrefix(imp) {
					t.Errorf("%s imports %q, which is not on the core dependency allowlist", dir, imp)
				}
			}
		}
	}
}

// isStdlib reports whether imp is a standard library path, meaning its first
// element has no dot.
func isStdlib(imp string) bool {
	first, _, _ := strings.Cut(imp, "/")
	return !strings.Contains(first, ".")
}

func hasAllowedPrefix(imp string) bool {
	for _, p := range allowedThirdParty {
		if imp == p || strings.HasPrefix(imp, p+"/") {
			return true
		}
	}
	return false
}
