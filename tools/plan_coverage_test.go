package tools

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParsePlanEntries_EntryNotObject(t *testing.T) {
	_, err := parsePlanEntries(map[string]any{"entries": []any{"not-an-object"}})
	require.Error(t, err)
	require.Contains(t, err.Error(), "must be an object")
}

func TestParsePlanMetadata_NotAMap(t *testing.T) {
	require.Nil(t, parsePlanMetadata(map[string]any{"metadata": "not-a-map"}))
}

func TestParsePlanMetadata_EmptyMap(t *testing.T) {
	require.Nil(t, parsePlanMetadata(map[string]any{"metadata": map[string]any{}}))
}

func TestParsePlanMetadata_CapsAtFiveKeys(t *testing.T) {
	md := map[string]any{}
	for i := 0; i < 8; i++ {
		md[string(rune('a'+i))] = "v"
	}
	out := parsePlanMetadata(map[string]any{"metadata": md})
	require.Len(t, out, 5)
}

func TestParsePlanMetadata_TruncatesLongValue(t *testing.T) {
	long := strings.Repeat("x", 200)
	out := parsePlanMetadata(map[string]any{"metadata": map[string]any{"k": long}})
	require.Len(t, out["k"], 80)
}
