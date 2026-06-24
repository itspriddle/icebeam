package cli

import (
	"flag"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/itspriddle/icebeam/internal/surface"
)

var update = flag.Bool("update", false, "update .surface file")

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok, "failed to determine caller file")
	return filepath.Join(filepath.Dir(file), "..", "..")
}

// TestSurface guards the CLI surface (commands, flags, positional arguments)
// against unintended changes. The expected snapshot lives in the repo-root
// .surface file; regenerate it with:
//
//	go test ./internal/cli/ -run TestSurface -update
//
// (or `make surface`) and commit the result when a change is intentional.
func TestSurface(t *testing.T) {
	root := NewRootCommand()
	got := surface.Generate(root)

	surfacePath := filepath.Join(repoRoot(t), ".surface")

	if *update {
		require.NoError(t, os.WriteFile(surfacePath, []byte(got), 0o644))
		t.Log("updated .surface file")
		return
	}

	want, err := os.ReadFile(surfacePath)
	require.NoError(t, err, ".surface file not found — run: go test ./internal/cli/ -run TestSurface -update")

	assert.Equal(t, string(want), got,
		"CLI surface has changed. If intentional, run:\n\n  go test ./internal/cli/ -run TestSurface -update\n\nand commit the updated .surface file.")
}
