// Forbidden-imports guard: keep github.com/kfet/agent and its tools
// subpackage free of heavyweight, product-shaped dependencies so the
// module stays a portable agent runtime.
//
// The module depends only on github.com/kfet/ai, github.com/kfet/pinexec,
// log/slog, and the standard library. This test asserts that the
// transitive import graph contains no third-party module other than the
// two sanctioned ones — a self-documenting boundary that fails loudly if
// a future edit drags in something heavier.

package agent_test

import (
	"os/exec"
	"strings"
	"testing"
)

// allowedModulePrefixes are the only non-stdlib import path prefixes the
// runtime and its tools subpackage may transitively depend on. The set is
// deliberately tiny: the portable AI primitives, the shell-exec helper,
// and the golang.org/x/ image+text packages the tools need for image
// resizing and Unicode-aware path handling.
var allowedModulePrefixes = []string{
	"github.com/kfet/ai",
	"github.com/kfet/pinexec",
	"golang.org/x/image",
	"golang.org/x/text",
}

// targets lists the packages whose import sets are checked.
var targets = []string{
	"github.com/kfet/agent",
	"github.com/kfet/agent/tools",
}

// isStdlib reports whether an import path belongs to the standard library
// (no dot in the first path segment).
func isStdlib(path string) bool {
	slash := strings.IndexByte(path, '/')
	first := path
	if slash >= 0 {
		first = path[:slash]
	}
	return !strings.Contains(first, ".")
}

// TestForbiddenImports asserts that the portability boundary on which the
// extraction depends has not eroded. It shells out to `go list` so the
// check sees the same import graph the compiler uses.
func TestForbiddenImports(t *testing.T) {
	for _, target := range targets {
		out, err := exec.Command("go", "list",
			"-deps",
			"-f", "{{.ImportPath}}",
			target,
		).CombinedOutput()
		if err != nil {
			t.Fatalf("go list %s: %v\n%s", target, err, out)
		}
		for _, line := range strings.Split(string(out), "\n") {
			path := strings.TrimSpace(line)
			if path == "" || isStdlib(path) {
				continue
			}
			// Self-imports (the module's own packages) are fine.
			if path == "github.com/kfet/agent" || strings.HasPrefix(path, "github.com/kfet/agent/") {
				continue
			}
			allowed := false
			for _, p := range allowedModulePrefixes {
				if path == p || strings.HasPrefix(path, p+"/") {
					allowed = true
					break
				}
			}
			if !allowed {
				t.Errorf("%s transitively imports disallowed package %s", target, path)
			}
		}
	}
}
