package integrations

import (
	"bufio"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const importPath = "github.com/callscreen/callscreen-platform/packages/go/integrations"

// TestNothingElseImportsThePlaceholders keeps this package's placeholders out of
// the product: no other Go file may import it, and no other go.mod may require
// it. The product's provider ports are named in the package comment.
func TestNothingElseImportsThePlaceholders(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "go.work")); statErr != nil {
		t.Fatalf("the repository root was not found from %s: %v", root, statErr)
	}
	self, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}

	var offenders []string
	scanned := 0
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "build", ".gradle", ".claude":
				return filepath.SkipDir
			}
			if path == self {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") && d.Name() != "go.mod" {
			return nil
		}
		scanned++
		if references(t, path) {
			rel, _ := filepath.Rel(root, path)
			offenders = append(offenders, filepath.ToSlash(rel))
		}
		return nil
	})
	if walkErr != nil {
		t.Fatal(walkErr)
	}
	if scanned < 100 {
		t.Fatalf("only %d Go files and go.mod files were scanned: the walk did not cover the repository", scanned)
	}
	if len(offenders) > 0 {
		t.Fatalf("these files import or require the placeholder integrations package; use the provider ports named in its package comment instead:\n  %s",
			strings.Join(offenders, "\n  "))
	}
}

// references reports whether the file names this package as an import (a quoted
// import path) or as a go.mod requirement. A mention in a comment is allowed.
func references(t *testing.T, path string) bool {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "//") {
			continue
		}
		if strings.Contains(line, `"`+importPath+`"`) || strings.HasPrefix(line, importPath+" ") || strings.HasPrefix(line, "require "+importPath) {
			return true
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return false
}
