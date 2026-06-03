package exchange

import (
	"encoding/json"
	"testing"
)

// TestActJSON_NoNestedAct_OmitsActKey is T-03's verification: an Act with
// no nested actor must serialise to exactly `{"sub":"x"}`, with no `act`
// key. This guards CONTRACT.md §6.1 rule 4 against silent drift in the
// JSON tag (an accidental drop of `omitempty` on Act.Act would surface
// here, not in production traffic).
func TestActJSON_NoNestedAct_OmitsActKey(t *testing.T) {
	got, err := json.Marshal(Act{Sub: "x"})
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}
	if want := `{"sub":"x"}`; string(got) != want {
		t.Fatalf("Act{Sub: \"x\"} marshal\n  got:  %s\n  want: %s", got, want)
	}
}
