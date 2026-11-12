package bootstrap

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Account-deletion codes stay unwired until decision O13: no production code
// of the identity binary (this package and cmd/server) installs a deletion
// sender or budget, asks the Sender for its deletion chain, or configures a
// provider's deletion template. Wiring them must be a deliberate change that
// removes this test, under O13.
func TestDeletionCodesAreNotWired(t *testing.T) {
	forbidden := map[string]bool{
		"SetDeletionCodeSender": true, "SetDeletionCodeBudget": true, "DeletionCodes": true,
		"DeletionFlowID": true, "DeletionTemplate": true,
	}
	var sites []string
	fset := token.NewFileSet()
	for _, dir := range []string{".", filepath.Join("..", "..", "cmd", "server")} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
				continue
			}
			f, err := parser.ParseFile(fset, filepath.Join(dir, e.Name()), nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			ast.Inspect(f, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.SelectorExpr:
					if forbidden[x.Sel.Name] {
						sites = append(sites, fset.Position(x.Pos()).String()+" "+x.Sel.Name)
					}
				case *ast.KeyValueExpr:
					if k, ok := x.Key.(*ast.Ident); ok && forbidden[k.Name] {
						sites = append(sites, fset.Position(x.Pos()).String()+" "+k.Name)
					}
				}
				return true
			})
		}
	}
	if len(sites) > 0 {
		t.Errorf("account-deletion codes are wired (decision O13 is open): %v", sites)
	}
}
