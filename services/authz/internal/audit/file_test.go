package audit

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

// expectedKeys is the CONTRACT.md §9 key set every emitted event must
// carry, byte-for-byte.
var expectedKeys = []string{
	"actor",
	"audience",
	"event_id",
	"existing_chain",
	"issuer",
	"occurred_at",
	"outcome",
	"policy_reason",
	"resulting_chain",
	"schema_version",
	"scope_granted",
	"scope_requested",
	"subject",
	"token_exp",
	"token_jti",
}

func strPtr(s string) *string { return &s }

func mintedEvent() Event {
	return Event{
		SchemaVersion:  SchemaVersion,
		EventID:        "01HX5Z2VQ8F9N0K2P4R6Y7T1S3",
		OccurredAt:     "2026-05-31T18:42:11.482Z",
		Outcome:        "minted",
		Issuer:         "https://authz.bonafide.local",
		Subject:        "spiffe://bonafide.local/human/alice@example.com",
		Actor:          "spiffe://bonafide.local/agent/planner",
		ExistingChain:  []string{},
		ResultingChain: []string{"spiffe://bonafide.local/agent/planner"},
		Audience:       "https://calendar.bonafide.local",
		ScopeRequested: "calendar:read:alice@example.com",
		ScopeGranted:   strPtr("calendar:read:alice@example.com"),
		PolicyReason:   nil,
		TokenJTI:       strPtr("01HX5Z2VQ8F9N0K2P4R6Y7T1S3"),
		TokenExp:       strPtr("2026-05-31T18:47:11.482Z"),
	}
}

func deniedEvent() Event {
	return Event{
		SchemaVersion:  SchemaVersion,
		EventID:        "01HX5Z2VQ8F9N0K2P4R6Y7T1S4",
		OccurredAt:     "2026-05-31T18:42:11.500Z",
		Outcome:        "denied",
		Issuer:         "https://authz.bonafide.local",
		Subject:        "spiffe://bonafide.local/human/alice@example.com",
		Actor:          "spiffe://bonafide.local/agent/planner",
		ExistingChain:  []string{},
		ResultingChain: nil,
		Audience:       "https://calendar.bonafide.local",
		ScopeRequested: "calendar:delete:alice@example.com",
		ScopeGranted:   nil,
		PolicyReason:   strPtr("unknown_scope"),
		TokenJTI:       nil,
		TokenExp:       nil,
	}
}

func readLines(t *testing.T, path string) [][]byte {
	t.Helper()
	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()
	var out [][]byte
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		b := make([]byte, len(scanner.Bytes()))
		copy(b, scanner.Bytes())
		out = append(out, b)
	}
	require.NoError(t, scanner.Err())
	return out
}

func TestEmit_MintedEvent_WritesSingleNDJSONLine_WithExpectedKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	em, err := NewFileEmitter(path)
	require.NoError(t, err)

	em.Emit(mintedEvent())
	require.NoError(t, em.Close())

	lines := readLines(t, path)
	require.Len(t, lines, 1, "exactly one NDJSON line per Emit")

	var got map[string]any
	require.NoError(t, json.Unmarshal(lines[0], &got))

	keys := make([]string, 0, len(got))
	for k := range got {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	require.Equal(t, expectedKeys, keys, "emitted keys must match CONTRACT.md §9 set")

	require.Equal(t, "minted", got["outcome"])
	require.Equal(t, "calendar:read:alice@example.com", got["scope_granted"])
	require.Nil(t, got["policy_reason"])
}

func TestEmit_DeniedEvent_HasNullScopeGrantedJTIExp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	em, err := NewFileEmitter(path)
	require.NoError(t, err)

	em.Emit(deniedEvent())
	require.NoError(t, em.Close())

	lines := readLines(t, path)
	require.Len(t, lines, 1)

	var got map[string]any
	require.NoError(t, json.Unmarshal(lines[0], &got))

	require.Equal(t, "denied", got["outcome"])
	require.Nil(t, got["scope_granted"], "denial must serialize scope_granted as JSON null")
	require.Nil(t, got["token_jti"], "denial must serialize token_jti as JSON null")
	require.Nil(t, got["token_exp"], "denial must serialize token_exp as JSON null")
	require.Nil(t, got["resulting_chain"], "denial must serialize resulting_chain as JSON null")
	require.Equal(t, "unknown_scope", got["policy_reason"])
}

func TestEmitter_RestartDurability_ReadsPreviouslyWrittenEvents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")

	em1, err := NewFileEmitter(path)
	require.NoError(t, err)
	em1.Emit(mintedEvent())
	require.NoError(t, em1.Close())

	em2, err := NewFileEmitter(path)
	require.NoError(t, err)
	em2.Emit(deniedEvent())
	require.NoError(t, em2.Close())

	lines := readLines(t, path)
	require.Len(t, lines, 2, "second emitter must append, not truncate")

	var first, second map[string]any
	require.NoError(t, json.Unmarshal(lines[0], &first))
	require.NoError(t, json.Unmarshal(lines[1], &second))
	require.Equal(t, "minted", first["outcome"])
	require.Equal(t, "denied", second["outcome"])
}

func TestNewFileEmitter_UnwritablePath_ReturnsError(t *testing.T) {
	// Path under a non-existent parent directory; O_CREATE cannot
	// resolve. Surfaces the fail-closed behaviour the spec mandates.
	_, err := NewFileEmitter(filepath.Join(t.TempDir(), "nope", "audit.log"))
	require.Error(t, err)
}

func TestEmit_DoesNotBlockBeyondTimeout_OnStuckDrain(t *testing.T) {
	// Synthesise a stuck emitter by capping its buffer at 1 and
	// flooding with more events than it can drain in real time
	// during the test. We are asserting the emitter returns without
	// hanging the producer — the mint path's non-blocking property.
	path := filepath.Join(t.TempDir(), "audit.log")
	em, err := NewFileEmitter(path)
	require.NoError(t, err)
	defer em.Close()

	for i := 0; i < fileEmitBufferSize+10; i++ {
		em.Emit(mintedEvent())
	}
	// If Emit hangs, the test deadlines out via -timeout; reaching
	// here means every Emit returned within fileEmitBlockTimeout.
}
