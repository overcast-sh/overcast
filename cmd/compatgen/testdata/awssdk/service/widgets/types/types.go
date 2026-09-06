// Package types stands in for an AWS SDK for Go v2 service's types package.
// It carries only what the fixture recipe's calls need, spelled the way
// smithy-go spells the real thing: an enum is a named string, a nested
// structure is a struct of pointers, and a union is an interface.
package types

// Color is an enum: a named string type, value-typed wherever it appears.
type Color string

// Enum values, as smithy-go generates them. Nothing reads these — the emitter
// writes the wire value through a conversion rather than looking a constant up
// — but leaving them out would make the fixture the one enum in the SDK with
// no constants, and a reader would wonder why.
const (
	ColorBlue Color = "blue"
	ColorRed  Color = "red"
)

// SprocketTag is a nested structure: a list member the emitter has to spell as
// a composite literal of its own.
type SprocketTag struct {
	Key   *string
	Value *string
}

// GaugeCursor is a union. smithy-go gives a union an interface with an
// unexported method, so no literal can build one, and there is nothing for the
// emitter to write — which is the refusal this type exists to prove. It also
// stands for the wider case: the vendored SDK's field type is the authority,
// and where the pinned snapshot calls a member a plain string (ListGauges'
// `Cursor` does) the SDK may still model it as something no literal spells.
type GaugeCursor interface {
	isGaugeCursor()
}

// GaugeTag is the {TagKey, TagValue} spelling detectTagShape's widening
// accepts alongside {Key, Value} — the pair KMS's own Tag structure uses.
type GaugeTag struct {
	TagKey   *string
	TagValue *string
}

// ValveTagKeyOnly is a key-only tag structure: the untag-list shape ELB
// Classic's RemoveTags uses (TagKeyOnly) instead of a plain string.
type ValveTagKeyOnly struct {
	Key *string
}
