package tools

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGroupAlive_ProcEdgeCases(t *testing.T) {
	KillBackgroundJobs()
	orig := procDir
	t.Cleanup(func() { procDir = orig })
	d := t.TempDir()
	procDir = d
	mk := func(name, stat string) {
		require.NoError(t, os.MkdirAll(filepath.Join(d, name), 0o755))
		if stat != "" {
			require.NoError(t, os.WriteFile(filepath.Join(d, name, "stat"), []byte(stat), 0o644))
		}
	}
	mk("self", "")                      // non-numeric
	mk("10", "")                        // no stat file
	mk("11", "garbage")                 // no ')'
	mk("12", "12 (a) S")                // too few fields
	mk("13", "13 (z) Z 1 77 0")         // zombie
	mk("14", "14 (x (y)) S 1 77 0")     // match, tricky comm
	mk("15", "15 (q) S 1 notanumber 0") // bad pgrp
	alive, pids := groupAlive(77)
	require.True(t, alive)
	require.Equal(t, []int{14}, pids)
	require.Equal(t, "1, 2", joinInts([]int{1, 2}))
}
