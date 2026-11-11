// AI-performed internal security assurance (2026-11-11); not an independent security review. docs/phase19/OD-12-REVISED-DIRECTION-2026-11-11.md §4.

package app

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

// Threat T-a: a predictable or biased one-time code. secureOtpCode must mint
// exactly six ASCII digits, drawn uniformly, from crypto/rand.

// saParseSources parses this package's non-test Go files from the syntax tree,
// so comments that name a package can neither trip nor mask these checks.
func saParseSources(t *testing.T) map[string]*ast.File {
	t.Helper()
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	files := map[string]*ast.File{}
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
		files[p] = f
	}
	if files["identity.go"] == nil {
		t.Fatal("identity.go was not found among the package sources")
	}
	return files
}

func saImports(f *ast.File) map[string]bool {
	out := map[string]bool{}
	for _, imp := range f.Imports {
		if p, err := strconv.Unquote(imp.Path.Value); err == nil {
			out[p] = true
		}
	}
	return out
}

// TestSecurityAssurance_OtpCodeIsSixUniformASCIIDigits draws 100 000 codes.
// Every one is six ASCII digits; each position's digit frequencies pass a
// deliberately loose chi-square bound (60 for 9 degrees of freedom, far beyond
// any plausible chance excursion, so it cannot flake), and the number of
// distinct codes is what a uniform draw from 10^6 gives (about 95 163).
func TestSecurityAssurance_OtpCodeIsSixUniformASCIIDigits(t *testing.T) {
	t.Parallel()
	const draws = 100_000
	var counts [otpDigits][10]int
	distinct := make(map[string]struct{}, draws)
	for i := 0; i < draws; i++ {
		c := secureOtpCode()
		if len(c) != 6 {
			t.Fatalf("code %q has %d bytes, want 6", c, len(c))
		}
		for p := 0; p < len(c); p++ {
			if c[p] < '0' || c[p] > '9' {
				t.Fatalf("code %q has a non-ASCII-digit byte at position %d", c, p)
			}
			counts[p][c[p]-'0']++
		}
		distinct[c] = struct{}{}
	}
	expected := float64(draws) / 10
	for p := range counts {
		chi := 0.0
		for _, n := range counts[p] {
			d := float64(n) - expected
			chi += d * d / expected
		}
		if chi > 60 {
			t.Errorf("position %d: chi-square %.1f > 60 over 9 d.o.f.; counts %v are not uniform", p, chi, counts[p])
		}
	}
	if len(distinct) < 90_000 {
		t.Errorf("%d distinct codes in %d draws; a uniform 10^6 space gives about 95 163", len(distinct), draws)
	}
}

// TestSecurityAssurance_OtpEntropySourceIsCryptoRand pins the source of the
// code's randomness: identity.go imports crypto/rand, secureOtpCode draws
// with rand.Int over rand.Reader, and no file of this package imports
// math/rand or math/rand/v2.
func TestSecurityAssurance_OtpEntropySourceIsCryptoRand(t *testing.T) {
	t.Parallel()
	files := saParseSources(t)
	if !saImports(files["identity.go"])["crypto/rand"] {
		t.Error("identity.go does not import crypto/rand")
	}
	for name, f := range files {
		imps := saImports(f)
		for _, weak := range []string{"math/rand", "math/rand/v2"} {
			if imps[weak] {
				t.Errorf("%s imports %s; one-time codes and tokens must come from crypto/rand", name, weak)
			}
		}
	}

	fn := saFunc(files["identity.go"], "secureOtpCode")
	if fn == nil {
		t.Fatal("secureOtpCode is not declared in identity.go")
	}
	drawsFromCryptoRand := false
	ast.Inspect(fn, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || !saIsSelector(call.Fun, "rand", "Int") || len(call.Args) != 2 {
			return true
		}
		if saIsSelector(call.Args[0], "rand", "Reader") {
			drawsFromCryptoRand = true
		}
		return true
	})
	if !drawsFromCryptoRand {
		t.Error("secureOtpCode does not call rand.Int(rand.Reader, ...)")
	}
}

// saFunc returns the function or method declared in f as name, or nil.
func saFunc(f *ast.File, name string) *ast.FuncDecl {
	for _, d := range f.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Name.Name == name {
			return fn
		}
	}
	return nil
}

// saIsSelector reports whether e is the selector pkg.sel.
func saIsSelector(e ast.Expr, pkg, sel string) bool {
	s, ok := e.(*ast.SelectorExpr)
	if !ok || s.Sel.Name != sel {
		return false
	}
	id, ok := s.X.(*ast.Ident)
	return ok && id.Name == pkg
}
