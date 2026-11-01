package perfstore

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// There is NO upload code anywhere in the performance system: data leaves the
// computer only when the owner or a user saves an export and sends the file
// themselves. This test is the guard: no file of a perf package, and no perf
// file of the app, imports anything that can open a connection.
func TestNoPackageOfThePerfSystemImportsTheNetwork(t *testing.T) {
	var files []string
	for _, dir := range []string{
		"../../perf", "../perflog", "../perfstore", "../perfagg", "../perfreport", "../perfmap", "../../cmd/arbor-perf",
	} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue // a package that does not exist yet has nothing to check
			}
			t.Fatal(err)
		}
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".go") {
				files = append(files, filepath.Join(dir, e.Name()))
			}
		}
	}
	// The app's own files for the diagnostics and the Performance window.
	appDir, err := os.ReadDir("../app")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range appDir {
		if strings.HasPrefix(e.Name(), "perf") && strings.HasSuffix(e.Name(), ".go") {
			files = append(files, filepath.Join("../app", e.Name()))
		}
	}
	if len(files) < 8 {
		t.Fatalf("only %d files found to check", len(files))
	}
	sort.Strings(files)
	forbidden := func(path string) bool {
		switch {
		case path == "net", strings.HasPrefix(path, "net/"):
			return true
		case path == "crypto/tls", strings.HasPrefix(path, "golang.org/x/net"):
			return true
		case strings.HasPrefix(path, "github.com/egoist/mygo/transfer"), strings.HasPrefix(path, "github.com/egoist/mygo/netproxy"):
			return true
		}
		return false
	}
	fset := token.NewFileSet()
	for _, f := range files {
		file, err := parser.ParseFile(fset, f, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		for _, imp := range file.Imports {
			if path := strings.Trim(imp.Path.Value, `"`); forbidden(path) {
				t.Errorf("%s imports %s: the performance system must have no upload code", f, path)
			}
		}
	}
}
