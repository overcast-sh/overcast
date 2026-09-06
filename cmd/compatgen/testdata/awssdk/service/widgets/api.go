// Package widgets stands in for an AWS SDK for Go v2 service package. The
// emitter resolves `<Op>Input`'s fields from here exactly as it resolves them
// from `service/sqs` in the go-sdk suite's own module, so the fixture proves
// the type-spelling table against real Go types rather than against a
// description of them.
//
// Only the input structs the fixture recipe reaches are declared, and the
// spellings deliberately spread across every branch of the table:
//
//	*string                    Description, Name, WidgetId, …   → aws.String
//	types.Color                CreateWidgetInput.Color          → types.Color("blue")
//	map[string]string          TagWidgetInput.Tags              → map[string]string{…}
//	[]string                   UntagWidgetInput.TagKeys         → []string{…}
//	[]types.SprocketTag        TagSprocketInput.Tags            → []types.SprocketTag{{…}}
//	[]types.GaugeTag           TagGaugeInput.Tags               → {TagKey, TagValue} spelling (KMS)
//	[]types.ValveTagKeyOnly    UntagValveInput.TagKeys          → key-only untag list (ELB)
//	*time.Time                 ListWidgetsInput.CreatedAfter    → refused (no literal)
//	int32                      RotateWidgetInput.Angle          → a bare literal; 0 is refused
//	types.GaugeCursor          ListGaugesInput.Cursor           → refused (a union)
//	(absent)                   FreezeWidgetInput.WidgetId       → refused (no such field)
//
// The client itself is not declared: the emitter reads input structs and
// nothing else, and the emitted source is never compiled against this module
// (the golden file carries a .golden suffix for that reason).
package widgets

import (
	"time"

	"github.com/aws/aws-sdk-go-v2/service/widgets/types"
)

type CreateWidgetInput struct {
	Color       types.Color
	Description *string
	Name        *string
}

type GetWidgetInput struct {
	WidgetId *string
}

type DescribeWidgetInput struct {
	WidgetId *string
}

type UpdateWidgetInput struct {
	Description *string
	WidgetId    *string
}

type TagWidgetInput struct {
	Tags     map[string]string
	WidgetId *string
}

type ListWidgetTagsInput struct {
	WidgetId *string
}

type UntagWidgetInput struct {
	TagKeys  []string
	WidgetId *string
}

type PolishWidgetInput struct {
	WidgetId *string
}

type ListWidgetsInput struct {
	CreatedAfter *time.Time
	NamePrefix   *string
}

type DeleteWidgetInput struct {
	WidgetId *string
}

type DescribeGaugeInput struct{}

type CalibrateGaugeInput struct {
	GaugeId *string
}

type CreateSprocketInput struct {
	Description *string
	Name        *string
}

type GetSprocketInput struct {
	SprocketId *string
}

type TagSprocketInput struct {
	SprocketId *string
	Tags       []types.SprocketTag
}

type ListSprocketTagsInput struct {
	SprocketId *string
}

type UntagSprocketInput struct {
	SprocketId *string
	TagKeys    []string
}

type DeleteSprocketInput struct {
	SprocketId *string
}

type ListCogsInput struct {
	NextToken *string
}

// ListGaugesInput's Cursor is a union rather than the string the pinned
// snapshot models, which is the refusal fixture for a type the table cannot
// spell.
type ListGaugesInput struct {
	Cursor types.GaugeCursor
}

type PingWidgetsInput struct{}

// RotateWidgetInput carries the one value-typed number in the fixture:
// smithy-go gives a member with a modeled default a value-typed field, which
// the SDK then serializes only when it is not the zero value.
type RotateWidgetInput struct {
	Angle    int32
	WidgetId *string
}

// FreezeWidgetInput deliberately has no field for the modeled `WidgetId`
// member: smithy-go's rename rule (reserved words, collisions) can spell a
// member differently from the model, and a member with no field is the
// refusal that used to compile and then fail at run time.
type FreezeWidgetInput struct{}

// TagGaugeInput, ListGaugeTagsInput and UntagGaugeInput exercise the
// {TagKey, TagValue} tag-structure spelling (types.GaugeTag) paired with an
// ordinary string-keyed untag list — the shape KMS's TagResource,
// ListResourceTags and UntagResource use.
type TagGaugeInput struct {
	GaugeId *string
	Tags    []types.GaugeTag
}

type ListGaugeTagsInput struct {
	GaugeId *string
}

type UntagGaugeInput struct {
	GaugeId *string
	TagKeys []string
}

type DescribeValveInput struct{}

// TagValveInput, ListValveTagsInput and UntagValveInput exercise ordinary
// {Key, Value} tags paired with an untag list of key-only structures
// (types.ValveTagKeyOnly) instead of bare strings — the shape ELB Classic's
// AddTags and RemoveTags use.
type TagValveInput struct {
	Tags    []types.SprocketTag
	ValveId *string
}

type ListValveTagsInput struct {
	ValveId *string
}

type UntagValveInput struct {
	TagKeys []types.ValveTagKeyOnly
	ValveId *string
}
