// Package audit owns the audit-emitter seam. In M1 the implementation
// is a file-backed NDJSON emitter (file.go); AUD swaps it for an HTTP
// emitter that POSTs to the control plane. The exchange handler
// depends only on the Emitter interface and never blocks the mint
// path on audit delivery — a CLAUDE.md "Safety constraints" tenet.
package audit

// SchemaVersion is the current value of the audit event schema
// (CONTRACT.md §9). Bumped on any breaking change to the event shape;
// callers do not override.
const SchemaVersion = "1"

// Emitter is the contract the handler depends on. Emit must not block
// the caller for longer than the implementation's bounded queue wait
// (100 ms for the M1 FileEmitter; see file.go).
type Emitter interface {
	Emit(event Event)
}

// Event mirrors CONTRACT.md §9 byte-for-byte. Conditional fields use
// pointer types so a nil value serializes as JSON `null` (the §9
// shape for those fields when the outcome dictates absence); required
// fields use plain types.
//
// The caller is responsible for initialising ExistingChain to a
// non-nil slice; a nil slice would marshal to JSON `null`, but §9
// specifies an empty array for first-hop exchanges. The handler in
// T-11 sets ExistingChain = []string{} when the subject_token has no
// prior act, then FlattenChain otherwise.
type Event struct {
	SchemaVersion  string   `json:"schema_version"`
	EventID        string   `json:"event_id"`
	OccurredAt     string   `json:"occurred_at"`
	Outcome        string   `json:"outcome"`
	Issuer         string   `json:"issuer"`
	Subject        string   `json:"subject"`
	Actor          string   `json:"actor"`
	ExistingChain  []string `json:"existing_chain"`
	ResultingChain []string `json:"resulting_chain"`
	Audience       string   `json:"audience"`
	ScopeRequested string   `json:"scope_requested"`
	ScopeGranted   *string  `json:"scope_granted"`
	PolicyReason   *string  `json:"policy_reason"`
	TokenJTI       *string  `json:"token_jti"`
	TokenExp       *string  `json:"token_exp"`
}
