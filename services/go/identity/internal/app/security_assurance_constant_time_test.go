// AI-performed internal security assurance (2026-11-11); not an independent security review. docs/phase19/OD-12-REVISED-DIRECTION-2026-11-11.md §4.

package app

import (
	"go/ast"
	"go/token"
	"testing"
)

// Threat T-i: a timing oracle on the code (or refresh-token) hash. The code
// hash must be compared only with a constant-time primitive, never with == or
// != on the strings.

// saMentions reports whether the expression subtree n names any of the
// identifiers or selector fields in names.
func saMentions(n ast.Node, names ...string) bool {
	found := false
	ast.Inspect(n, func(x ast.Node) bool {
		if id, ok := x.(*ast.Ident); ok {
			for _, want := range names {
				if id.Name == want {
					found = true
				}
			}
		}
		return !found
	})
	return found
}

// saPlainComparisons returns every == or != in n whose operands name any of
// names.
func saPlainComparisons(n ast.Node, names ...string) []*ast.BinaryExpr {
	var out []*ast.BinaryExpr
	ast.Inspect(n, func(x ast.Node) bool {
		b, ok := x.(*ast.BinaryExpr)
		if ok && (b.Op == token.EQL || b.Op == token.NEQ) && (saMentions(b.X, names...) || saMentions(b.Y, names...)) {
			out = append(out, b)
		}
		return true
	})
	return out
}

// saConstantTimeCalls returns the hmac.Equal and subtle.ConstantTimeCompare
// calls in n.
func saConstantTimeCalls(n ast.Node) []*ast.CallExpr {
	var out []*ast.CallExpr
	ast.Inspect(n, func(x ast.Node) bool {
		c, ok := x.(*ast.CallExpr)
		if ok && (saIsSelector(c.Fun, "hmac", "Equal") || saIsSelector(c.Fun, "subtle", "ConstantTimeCompare")) {
			out = append(out, c)
		}
		return true
	})
	return out
}

// TestSecurityAssurance_CodeHashIsComparedInConstantTime: in checkCode the
// submitted code's hash is compared with challenge.CodeHash through
// hmac.Equal, and nowhere in the package's non-test sources is CodeHash (or a
// codeHash variable) compared with == or !=.
func TestSecurityAssurance_CodeHashIsComparedInConstantTime(t *testing.T) {
	t.Parallel()
	files := saParseSources(t)

	fn := saFunc(files["identity.go"], "checkCode")
	if fn == nil {
		t.Fatal("checkCode is not declared in identity.go")
	}
	viaHmac := false
	for _, c := range saConstantTimeCalls(fn.Body) {
		if saIsSelector(c.Fun, "hmac", "Equal") && len(c.Args) == 2 && (saMentions(c.Args[0], "CodeHash") || saMentions(c.Args[1], "CodeHash")) {
			viaHmac = true
		}
	}
	if !viaHmac {
		t.Error("checkCode does not compare the code hash with hmac.Equal(..., challenge.CodeHash)")
	}

	for name, f := range files {
		for _, b := range saPlainComparisons(f, "CodeHash", "codeHash") {
			t.Errorf("%s: a code hash is compared with %s (not constant time) at offset %d", name, b.Op, b.OpPos)
		}
	}
}

// TestSecurityAssurance_EqualHashIsConstantTime: equalHash, which compares
// refresh-token hashes, delegates to a constant-time primitive and contains no
// == or != at all; and the three refresh-token checks against a PRESENTED
// token (RefreshToken, the proof-bound refresh and in-app deletion) go through
// equalHash.
func TestSecurityAssurance_EqualHashIsConstantTime(t *testing.T) {
	t.Parallel()
	files := saParseSources(t)

	fn := saFunc(files["identity.go"], "equalHash")
	if fn == nil {
		t.Fatal("equalHash is not declared in identity.go")
	}
	if len(saConstantTimeCalls(fn.Body)) == 0 {
		t.Error("equalHash does not use hmac.Equal or subtle.ConstantTimeCompare")
	}
	ast.Inspect(fn.Body, func(x ast.Node) bool {
		if b, ok := x.(*ast.BinaryExpr); ok && (b.Op == token.EQL || b.Op == token.NEQ) {
			t.Errorf("equalHash contains a plain %s comparison", b.Op)
		}
		return true
	})

	for _, c := range []struct{ file, fn string }{
		{"identity.go", "RefreshToken"},
		{"identity.go", "RefreshTokenWithDeviceProofAndIntegrity"},
		{"erasure.go", "DeleteAccountWithDeviceProof"},
	} {
		f := files[c.file]
		if f == nil {
			t.Errorf("%s is not among the package sources", c.file)
			continue
		}
		d := saFunc(f, c.fn)
		if d == nil {
			t.Errorf("%s is not declared in %s", c.fn, c.file)
			continue
		}
		saAssertPresentedHashViaEqualHash(t, c.file+":"+c.fn, d)
	}
}

// saAssertPresentedHashViaEqualHash: n compares a session's RefreshTokenHash
// with presentedHash through equalHash, and never with == or !=.
func saAssertPresentedHashViaEqualHash(t *testing.T, where string, n ast.Node) {
	t.Helper()
	viaEqualHash := false
	ast.Inspect(n, func(x ast.Node) bool {
		c, ok := x.(*ast.CallExpr)
		if !ok {
			return true
		}
		if id, isIdent := c.Fun.(*ast.Ident); isIdent && id.Name == "equalHash" && saMentions(c, "presentedHash") {
			viaEqualHash = true
		}
		return true
	})
	if !viaEqualHash {
		t.Errorf("%s does not compare the presented refresh-token hash through equalHash", where)
	}
	for _, b := range saPlainComparisons(n, "presentedHash") {
		t.Errorf("%s compares the presented refresh-token hash with %s", where, b.Op)
	}
}
