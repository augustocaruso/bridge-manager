package chatty

import (
	"path/filepath"
	"testing"
)

func canonicalTempDir(t *testing.T) string {
	t.Helper()
	// Production rejects symlinked ancestors. Only fixture roots are resolved,
	// so deliberate symlinks created below them still exercise that rejection.
	// See CHATTY.md: Sources And Verification.
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}
