package middleware

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// parsePackageSources parses the package's non-test files. Declarations and
// literals are read from the syntax tree, so a comment that names a removed
// symbol (as the package doc does) can neither trip nor mask these checks.
func parsePackageSources(t *testing.T) []*ast.File {
	t.Helper()
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var files []*ast.File
	for _, p := range paths {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		src, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, p, src, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}
	if len(files) == 0 {
		t.Fatal("no package sources found")
	}
	return files
}

// TestNoUnverifiedAuthStubIsDeclared: AuthMiddleware accepted any "Bearer "
// header and attached a hardcoded identity, and RequireRole enforced roles on
// that fabricated identity. Both had no callers and were removed (Phase 18
// D11.1). Real authentication needs a verifier and a key source, which this
// package has neither of, so nothing auth-shaped may be declared here.
func TestNoUnverifiedAuthStubIsDeclared(t *testing.T) {
	t.Parallel()
	removed := map[string]bool{"AuthMiddleware": true, "RequireRole": true, "UserContextKey": true}
	authShaped := []string{"auth", "role", "token", "verify", "permission"}
	for _, f := range parsePackageSources(t) {
		for _, d := range f.Decls {
			for _, name := range declaredNames(d) {
				if removed[name] {
					t.Errorf("%s was removed as an unverifying auth stub and must not return", name)
					continue
				}
				lower := strings.ToLower(name)
				for _, w := range authShaped {
					if strings.Contains(lower, w) {
						t.Errorf("%s looks like an authentication or authorisation control; this package has no verifier", name)
						break
					}
				}
			}
		}
	}
}

// TestNoHardcodedIdentityIsFabricated pins the literal identity the removed
// stub attached to every request.
func TestNoHardcodedIdentityIsFabricated(t *testing.T) {
	t.Parallel()
	fabricated := map[string]bool{"usr_subscriber": true, "ten_in_south": true, "subscriber": true}
	for _, f := range parsePackageSources(t) {
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			if v, err := strconv.Unquote(lit.Value); err == nil && fabricated[v] {
				t.Errorf("fabricated identity literal %q is back", v)
			}
			return true
		})
	}
}

// declaredNames returns the top-level identifiers a declaration introduces,
// including methods.
func declaredNames(d ast.Decl) []string {
	switch d := d.(type) {
	case *ast.FuncDecl:
		return []string{d.Name.Name}
	case *ast.GenDecl:
		var names []string
		for _, s := range d.Specs {
			switch s := s.(type) {
			case *ast.TypeSpec:
				names = append(names, s.Name.Name)
			case *ast.ValueSpec:
				for _, n := range s.Names {
					names = append(names, n.Name)
				}
			}
		}
		return names
	}
	return nil
}
