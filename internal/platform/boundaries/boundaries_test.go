// Package boundaries asserts the import rules that make the service split
// real rather than a directory convention (Article IX).
package boundaries

import (
	"os/exec"
	"strings"
	"testing"
)

const module = "github.com/donaina/echo/"

// TestImportBoundaries walks the real import graph. It is a static check, so
// it runs in the fast suite and fails the build, not a review.
func TestImportBoundaries(t *testing.T) {
	out, err := exec.Command("go", "list", "-f", `{{.ImportPath}}|{{join .Imports ","}}`, "./...").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}

	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		pkg, imports, ok := strings.Cut(line, "|")
		if !ok {
			continue
		}
		owner, ok := internalOwner(pkg)
		if !ok {
			continue
		}
		for _, imp := range strings.Split(imports, ",") {
			target, ok := internalOwner(imp)
			if !ok {
				continue
			}
			switch {
			case owner == "platform" && target != "platform":
				t.Errorf("%s imports %s: internal/platform may not depend on a service", pkg, imp)
			case owner != "platform" && target != "platform" && owner != target:
				t.Errorf("%s imports %s: services talk over the wire, not through imports", pkg, imp)
			}
		}
	}
}

// internalOwner returns the first path element under internal/, if any.
func internalOwner(path string) (string, bool) {
	rest, ok := strings.CutPrefix(path, module+"internal/")
	if !ok {
		return "", false
	}
	owner, _, _ := strings.Cut(rest, "/")
	return owner, true
}
