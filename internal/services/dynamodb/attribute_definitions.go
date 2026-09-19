package dynamodb

// attribute_definitions.go holds CreateTable's consistency check between
// AttributeDefinitions and the key schemas that use them (issue #1707, rules
// 1 and 2). AWS requires the two to describe exactly the same set of
// attributes: every attribute named by the table's KeySchema or by any
// index's KeySchema must be defined, and nothing else may be — DynamoDB is
// schemaless outside the keys, so a definition no key uses is a mistake AWS
// catches at the earliest possible point rather than a harmless hint.
//
// AWS has four wordings for the ways the two sets can disagree, chosen by
// comparing their sizes and by whether the request declares any index. The
// decision table below is moto's _throw_attr_error
// (moto/dynamodb/responses.py), and three of the four messages are also
// quoted verbatim from real AWS elsewhere:
//   - "Some AttributeDefinitions are not used. AttributeDefinitions: [...],
//     keys used: [...]" — aws-cloudformation/cfn-lint#1037.
//   - "Some index key attributes are not defined in AttributeDefinitions.
//     Keys: [...], AttributeDefinitions: [...]" — getmoto/moto#2445.
//   - "Number of attributes in KeySchema does not exactly match number of
//     attributes defined in AttributeDefinitions" —
//     https://rory.horse/posts/dynamo-dissected-schema/.
//   - "Invalid KeySchema: Some index key attribute have no definition" — moto
//     only; its grammar is AWS's, not moto's.
//
// The bracketed lists are Java's List.toString form — "[a, b]" — which is
// what AWS's responses carry.

import (
	"strings"

	"github.com/overcast-sh/overcast/internal/protocol"
)

// validateAttributeDefinitions checks that req.AttributeDefinitions and the
// key schemas of the table and its indexes name the same attributes,
// returning AWS's ValidationException for the disagreement or nil.
func validateAttributeDefinitions(req *createTableRequest) *protocol.AWSError {
	defined := make([]string, 0, len(req.AttributeDefinitions))
	definedSet := make(map[string]bool, len(req.AttributeDefinitions))
	for _, d := range req.AttributeDefinitions {
		if !definedSet[d.AttributeName] {
			definedSet[d.AttributeName] = true
			defined = append(defined, d.AttributeName)
		}
	}

	// Every key attribute in use, table first then indexes in request order,
	// each name once — AWS's "keys used" list is in this order.
	var used []string
	usedSet := make(map[string]bool)
	addKeys := func(schema []KeySchemaElement) {
		for _, k := range schema {
			if !usedSet[k.AttributeName] {
				usedSet[k.AttributeName] = true
				used = append(used, k.AttributeName)
			}
		}
	}
	addKeys(req.KeySchema)
	for i := range req.GlobalSecondaryIndexes {
		addKeys(req.GlobalSecondaryIndexes[i].KeySchema)
	}
	for i := range req.LocalSecondaryIndexes {
		addKeys(req.LocalSecondaryIndexes[i].KeySchema)
	}

	var missing []string
	for _, name := range used {
		if !definedSet[name] {
			missing = append(missing, name)
		}
	}
	unused := false
	for _, name := range defined {
		if !usedSet[name] {
			unused = true
			break
		}
	}
	if len(missing) == 0 && !unused {
		return nil
	}

	hasIndexes := len(req.GlobalSecondaryIndexes) > 0 || len(req.LocalSecondaryIndexes) > 0
	switch {
	case len(defined) > len(used):
		if hasIndexes {
			return errValidation(invalidParameterPrefix +
				"Some AttributeDefinitions are not used. AttributeDefinitions: " + javaList(defined) +
				", keys used: " + javaList(used))
		}
		return errValidation(invalidParameterPrefix +
			"Number of attributes in KeySchema does not exactly match number of attributes defined in AttributeDefinitions")
	case len(defined) < len(used):
		if hasIndexes {
			return errValidation(invalidParameterPrefix +
				"Some index key attributes are not defined in AttributeDefinitions. Keys: " + javaList(missing) +
				", AttributeDefinitions: " + javaList(defined))
		}
		return errValidation("Invalid KeySchema: Some index key attribute have no definition")
	default:
		// Same count, different names: with indexes AWS lists only the
		// undefined keys, without them it lists every key.
		keys := used
		if hasIndexes {
			keys = missing
		}
		return errValidation(invalidParameterPrefix +
			"Some index key attributes are not defined in AttributeDefinitions. Keys: " + javaList(keys) +
			", AttributeDefinitions: " + javaList(defined))
	}
}

// javaList formats names the way AWS's messages carry them: "[a, b]".
func javaList(names []string) string {
	return "[" + strings.Join(names, ", ") + "]"
}
