package dynamodb

// item_size.go measures an item the way DynamoDB bills and bounds it, so the
// 400 KB item-size ceiling (issue #1707, rule 8) is enforced on what AWS
// would count rather than on the JSON the request happened to arrive in —
// numbers in particular are not their decimal text length, and binary
// values are their raw bytes, not their base64.
//
// The rules, from "DynamoDB item sizes and formats" in the Developer Guide
// (https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/CapacityUnitCalculations.html):
//
//   - An item's size is the sum of its attribute names' UTF-8 lengths plus
//     the size of each value.
//   - String: the number of UTF-8 bytes.
//   - Number: 1 byte per two significant digits, plus 1 byte. Leading and
//     trailing zeroes are trimmed first, so "0100.00" has one significant
//     digit ("1") and costs 2 bytes.
//   - Binary: the number of raw (base64-decoded) bytes.
//   - Boolean and Null: 1 byte.
//   - List and Map: 3 bytes of overhead, plus 1 byte per element, plus the
//     size of each element — a Map element's size includes its key's UTF-8
//     length, exactly as a top-level attribute's does.
//   - Sets: the sum of the element sizes, each measured by the scalar rule
//     for its type, with no overhead.
//
// The 100-byte per-item storage overhead that page also describes is a
// billing figure, not part of the 400 KB request limit, so it is not
// counted here. AWS calls its number rule "approximately"; the boundary is
// therefore right to within a byte or two per number, which is the
// precision a request-size ceiling needs.

import (
	"encoding/base64"
	"strings"

	"github.com/overcast-sh/overcast/internal/protocol"
)

// maxItemSizeBytes is DynamoDB's per-item ceiling: 400 KB, counted by
// itemSizeBytes' rules. An item whose size exceeds it is rejected before
// anything is written.
const maxItemSizeBytes = 400 * 1024

// itemSizeBytes returns item's size in bytes under DynamoDB's accounting
// (see the file header for the rules).
func itemSizeBytes(item Item) int {
	size := 0
	for name, v := range item {
		size += len(name) + attrValueSizeBytes(v)
	}
	return size
}

// attrValueSizeBytes returns the size of one attribute value, without its
// name. A malformed value (no type tag, or a payload of the wrong Go type)
// counts as zero rather than failing: sizing is a bound, and the request
// decoder is where shape faults are answered.
func attrValueSizeBytes(v attrValue) int {
	for typ, raw := range v {
		switch typ {
		case "S":
			s, _ := raw.(string)
			return len(s)
		case "N":
			s, _ := raw.(string)
			return numberSizeBytes(s)
		case "B":
			s, _ := raw.(string)
			return binarySizeBytes(s)
		case "BOOL", "NULL":
			return 1
		case "SS", "NS", "BS":
			elems, _ := raw.([]any)
			sum := 0
			for _, e := range elems {
				s, _ := e.(string)
				switch typ {
				case "SS":
					sum += len(s)
				case "NS":
					sum += numberSizeBytes(s)
				default:
					sum += binarySizeBytes(s)
				}
			}
			return sum
		case "L":
			elems, _ := raw.([]any)
			sum := 3
			for _, e := range elems {
				sum++
				if av, ok := anyToAttrValue(e); ok {
					sum += attrValueSizeBytes(av)
				}
			}
			return sum
		case "M":
			m, _ := raw.(map[string]any)
			sum := 3
			for k, e := range m {
				sum += 1 + len(k)
				if av, ok := anyToAttrValue(e); ok {
					sum += attrValueSizeBytes(av)
				}
			}
			return sum
		}
	}
	return 0
}

// numberSizeBytes sizes a Number by its significant digits: the digits of
// the mantissa with leading and trailing zeroes trimmed, at two per byte,
// plus one. Sign, decimal point and any exponent carry no digits of their
// own. Zero has no significant digits and costs the one trailing byte.
func numberSizeBytes(s string) int {
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimLeft(s, "+-")
	s = strings.ReplaceAll(s, ".", "")
	s = strings.Trim(s, "0")
	return (len(s)+1)/2 + 1
}

// binarySizeBytes sizes a Binary by its decoded length. A value that is not
// valid base64 is sized by the length its base64 text would decode to,
// which is the closest bound available without a payload to measure.
func binarySizeBytes(s string) int {
	if n, err := base64.StdEncoding.DecodeString(s); err == nil {
		return len(n)
	}
	return base64.StdEncoding.DecodedLen(len(s))
}

// errItemSizeExceeded is AWS's answer to a PutItem (or a batch put) whose
// item is over maxItemSizeBytes.
func errItemSizeExceeded() *protocol.AWSError {
	return errValidation("Item size has exceeded the maximum allowed size")
}

// errItemSizeToUpdateExceeded is AWS's answer to an UpdateItem whose result
// would be over maxItemSizeBytes — the wording the TransactWriteItems API
// reference lists under its ValidationError cancellation reasons.
func errItemSizeToUpdateExceeded() *protocol.AWSError {
	return errValidation("Item size to update has exceeded the maximum allowed size")
}
