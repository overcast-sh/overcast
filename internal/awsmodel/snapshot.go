package awsmodel

import "encoding/json"

// The pruned shape snapshot's vocabulary.
//
// cmd/awsmodelgen prunes an upstream Smithy model down to one committed
// document per in-scope service; cmd/compatgen reads those documents back.
// The three types below are that document's contract, declared once so the
// writer's field set and the reader's cannot drift — they were two
// declarations held level by a comment, which is not a mechanism.
//
// Nothing here reads a snapshot: this file states the format, and the
// commands under cmd/ own the I/O. The snapshot directory is named in
// cmd/awsmodelgen/README.md rather than in this package, because
// internal/awsapi's TestShapeSnapshot_isGeneratorInputOnly greps every
// non-test Go file outside cmd/ for that path to prove no runtime package
// can read model data.

// Snapshot is one service's pruned document.
//
// Fields are declared in the order the committed file carries them, and
// encoding/json preserves declaration order while sorting map keys, so a
// document is byte-stable across regenerations.
type Snapshot struct {
	Smithy       string                   `json:"smithy"`
	Service      string                   `json:"service"`
	Namespace    string                   `json:"namespace"`
	ServiceShape string                   `json:"serviceShape"`
	SDKID        string                   `json:"sdkId"`
	APIVersion   string                   `json:"apiVersion"`
	Protocols    []string                 `json:"protocols"`
	Shapes       map[string]SnapshotShape `json:"shapes"`
}

// SnapshotShape is one pruned shape. It keeps the Smithy AST vocabulary —
// type, members, member/key/value, input/output/errors and the whole resource
// lifecycle — so a consumer decodes the snapshot with the same structs it
// would use for the raw AST, with two differences.
//
// Shape references are strings rather than {"target": …} objects, and they are
// namespace-relative: every reference into the service's own namespace loses
// the `com.amazonaws.<svc>#` prefix, which is recorded once in Snapshot. A
// reference into another namespace stays fully qualified, so a relative name is
// unambiguous — it never contains '#'.
//
// Traits are filtered to the pruner's allowlist and their values are decoded
// and re-encoded, so object keys inside a trait come out sorted.
type SnapshotShape struct {
	Type                 string                     `json:"type"`
	Version              string                     `json:"version,omitempty"`
	Input                string                     `json:"input,omitempty"`
	Output               string                     `json:"output,omitempty"`
	Errors               []string                   `json:"errors,omitempty"`
	Member               string                     `json:"member,omitempty"`
	Key                  string                     `json:"key,omitempty"`
	Value                string                     `json:"value,omitempty"`
	Identifiers          map[string]string          `json:"identifiers,omitempty"`
	Properties           map[string]string          `json:"properties,omitempty"`
	Create               string                     `json:"create,omitempty"`
	Put                  string                     `json:"put,omitempty"`
	Read                 string                     `json:"read,omitempty"`
	Update               string                     `json:"update,omitempty"`
	Delete               string                     `json:"delete,omitempty"`
	List                 string                     `json:"list,omitempty"`
	Operations           []string                   `json:"operations,omitempty"`
	CollectionOperations []string                   `json:"collectionOperations,omitempty"`
	Resources            []string                   `json:"resources,omitempty"`
	Members              map[string]SnapshotMember  `json:"members,omitempty"`
	Traits               map[string]json.RawMessage `json:"traits,omitempty"`

	// MemberTraits, KeyTraits and ValueTraits are the allowlisted traits on a
	// list's member and a map's key and value. They are carried for in-process
	// consumers of the pruner — the runtime SDK shape tables need a list
	// member's @xmlName — and are never rendered into the committed snapshot,
	// whose format has no place for them.
	MemberTraits map[string]json.RawMessage `json:"-"`
	KeyTraits    map[string]json.RawMessage `json:"-"`
	ValueTraits  map[string]json.RawMessage `json:"-"`
}

// SnapshotMember is one structure or union member: the shape it targets, and
// the allowlisted traits declared on the member itself.
type SnapshotMember struct {
	Target string                     `json:"target"`
	Traits map[string]json.RawMessage `json:"traits,omitempty"`
}
