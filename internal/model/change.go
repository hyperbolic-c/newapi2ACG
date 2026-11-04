package model

// Op describes a logical target operation, independent of its HTTP API.
type Op string

const (
	Create  Op = "create"
	Update  Op = "update"
	Disable Op = "disable"
	Delete  Op = "delete"
	Grant   Op = "grant"
	Revoke  Op = "revoke"
)

type Outcome string

const (
	Succeeded Outcome = "succeeded"
	Failed    Outcome = "failed"
	Retryable Outcome = "retryable"
	Unknown   Outcome = "unknown"
)

type Change struct {
	// OpID is persisted for one intent and reused on retries. A later intent,
	// even returning to earlier content, must receive a new OpID.
	OpID     string
	Op       Op
	Resource Resource
	Prev     *Resource
	// IdemKey binds source identity and content; remote request deduplication
	// must additionally include OpID and Op to avoid swallowing later changes.
	IdemKey string
}
type Result struct {
	OpID     string
	Outcome  Outcome
	TargetID string
	Err      error
}

// Observation.Resource is a partial view: fields named in Unreadable are not
// usable for drift comparisons. ContentHash is a receipt, not a secret readback.
type Observation struct {
	TargetID   string
	Resource   Resource
	Unreadable []string
}
type Observed struct{ Items []Observation }
