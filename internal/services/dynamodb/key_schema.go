package dynamodb

// key_schema.go holds the one check that every operation accepting an item or
// a primary key runs before it touches storage: the attributes named in the
// table's KeySchema must be present, and each must carry the type its
// AttributeDefinitions entry declares. Everything outside the key schema is
// untyped — DynamoDB is schemaless there and no constraint may be imposed on
// it (issue #1637).
//
// This matters beyond the missing error. The storage encoding of a key
// component depends on its declared type (key_order.go's
// encodeStorageKeyComponent runs Number components through
// numeric_encoding.go), so an N-typed key accepted as an S was written under
// an encoding a correctly typed GetItem never looks under — the write
// succeeded and the item was unreadable.
//
// AWS evidence for the messages below:
//   - "One or more parameter values were invalid: Type mismatch for key
//     <name> expected: <type> actual: <type>" and "One or more parameter
//     values were invalid: Missing the key <name> in the item" are what
//     PutItem answers, the latter reported verbatim against real AWS in
//     aws/aws-sdk-js#4057. moto raises both from its Table.put_item
//     (InvalidAttributeTypeError; MockValidationException).
//   - "The provided key element does not match the schema" is AWS's single
//     answer for a supplied Key map that disagrees with the schema in any
//     way — wrong type, missing key attribute, or an attribute the key schema
//     does not name (AWS re:Post "Troubleshoot 'provided key element' error in
//     DynamoDB"; moto's ProvidedKeyDoesNotExist).
//   - "One or more parameter values were invalid: Condition parameter type
//     does not match schema type" is what a Query whose KeyConditionExpression
//     compares a key against the wrong type answers.
//
// Rows written before this check existed are left alone, and deliberately so.
// A mistyped key is already stored under the encoding its supplied type
// implies, which is not the one a correctly typed request looks under — so it
// was unreadable by key before this change and still is. There is nothing to
// migrate it to: real AWS could never have held such a row, and the value that
// made it mistyped ("not-a-number" in an N-typed key) has no correct form to
// rewrite it into. Scan still lists those rows and DeleteTable still removes
// them; the TTL sweeper reaches them too, because it deletes by an item it
// read back out of storage rather than by a caller-supplied key.
//
// Not covered, deliberately: the key attributes of a GSI or LSI (#1681). AWS
// type-checks those against AttributeDefinitions too once the index exists,
// but its message for that case ("Type mismatch for Index Key …") could not be
// confirmed from a source good enough to encode, and guessing it would trade
// one wrong behaviour for another. Index keys are also legitimately *absent*
// from an item — that is what makes a secondary index sparse, and the LSI/GSI
// read-path tests rely on it — so the presence half of this check must never
// be extended to them.

import (
	"fmt"

	"github.com/overcast-sh/overcast/internal/protocol"
)

// keyOperand says which of the two shapes a caller supplied the primary key
// in. AWS reports a fault differently for each, because they are different
// promises: an Item may carry any number of non-key attributes alongside the
// key, whereas a Key is exactly the key schema and nothing else.
type keyOperand int

const (
	// operandItem is a whole item — PutItem's Item, BatchWriteItem's
	// PutRequest.Item, a transaction Put's Item. Only the key attributes are
	// checked; every other attribute is untyped and unconstrained.
	operandItem keyOperand = iota
	// operandKey is a bare primary key — GetItem/DeleteItem/UpdateItem's Key,
	// BatchGetItem's Keys, BatchWriteItem's DeleteRequest.Key, and a
	// transaction's Get/Delete/Update/ConditionCheck Key. It must name the key
	// schema's attributes and no others.
	operandKey
)

// validateKeySchema checks keyOrItem against table's key schema, returning the
// ValidationException AWS returns when it does not match, or nil.
//
// It is O(the number of key attributes) — at most two — and reads nothing:
// the caller has already loaded the table, and both the declared types and the
// supplied values are in hand.
//
// A table whose stored descriptor declares no partition key is left alone
// rather than rejected here: that is a malformed or legacy record, and
// resolveKeys already answers it with "Table has no hash key defined." further
// down. So is a key attribute with no AttributeDefinitions entry — AWS makes
// that impossible at CreateTable, and inventing a type for it would reject
// writes an existing store may already depend on (the same defensive default
// keyAttrType takes for ordering).
func validateKeySchema(table *Table, keyOrItem Item, operand keyOperand) *protocol.AWSError {
	if table == nil {
		return nil
	}

	// The key attributes, partition key first, so a request that gets both
	// wrong is reported against the same one AWS reports.
	var keyNames [2]string
	n := 0
	if hashName := table.hashKeyName(); hashName != "" {
		keyNames[n] = hashName
		n++
	}
	if sortName := table.sortKeyName(); sortName != "" {
		keyNames[n] = sortName
		n++
	}
	if n == 0 {
		return nil
	}

	// A Key is exactly the key schema, so anything the schema does not name
	// is as wrong as a missing one. Comparing the sizes catches an extra
	// attribute; the presence loop below catches a missing one.
	if operand == operandKey && len(keyOrItem) != n {
		return errKeyElementMismatch()
	}

	// Presence before type, both in partition-then-sort order: AWS reports a
	// missing partition key even when the sort key is also mistyped.
	for _, name := range keyNames[:n] {
		if _, ok := keyOrItem[name]; ok {
			continue
		}
		if operand == operandKey {
			return errKeyElementMismatch()
		}
		return errValidation(invalidParameterPrefix + "Missing the key " + name + " in the item")
	}

	for _, name := range keyNames[:n] {
		declared, actual, mismatched := keyTypeMismatch(table, name, keyOrItem[name])
		if !mismatched {
			continue
		}
		if operand == operandKey {
			return errKeyElementMismatch()
		}
		return errValidation(fmt.Sprintf("%sType mismatch for key %s expected: %s actual: %s",
			invalidParameterPrefix, name, declared, actual))
	}

	// A key attribute may not be empty (issue #1707, rule 3). Since May 2020
	// AWS accepts empty String and Binary values everywhere except in the
	// key attributes of the table and its indexes
	// (https://aws.amazon.com/about-aws/whats-new/2020/05/amazon-dynamodb-now-supports-empty-values-for-non-key-string-and-binary-attributes-in-dynamodb-tables),
	// and answers an empty key with the message below, which moto raises
	// from put_item, update_item and batch_write_item. Overcast applies it
	// to a supplied Key as well as to an Item: an empty key value can never
	// name an item, so a read by one is the same fault as a write of one.
	// Index key attributes are not checked here, for the reason the file
	// header gives.
	for _, name := range keyNames[:n] {
		if isEmptyScalar(keyOrItem[name]) {
			return errValidation("One or more parameter values are not valid. " +
				"The AttributeValue for a key attribute cannot contain an empty string value. Key: " + name)
		}
	}

	return nil
}

// isEmptyScalar reports whether v is a String or Binary value of length
// zero — the two types whose emptiness AWS constrains.
func isEmptyScalar(v attrValue) bool {
	if len(v) != 1 {
		return false
	}
	switch attrType(v) {
	case "S", "B":
		return extractScalar(v) == ""
	}
	return false
}

// validateKeyConditionSchema checks that a compiled KeyConditionExpression
// constrains the partition key actually in play — the table's for a
// base-table Query, the index's when IndexName is set — returning AWS's
// "Query condition missed key schema element: <name>" ValidationException
// otherwise (issue #1707, rule 4). Without it a condition on the sort key
// alone, or on a non-key attribute, was compiled as if its attribute were
// the partition key and answered with whatever that lookup found, teaching
// a data model DynamoDB cannot serve.
//
// The parser reads the first equality as the partition-key condition, but
// AWS accepts the two conditions in either order, so a "sk = :s AND pk =
// :p" is recognised here and its halves swapped in place before the check.
//
// Message source: https://dynobase.dev/dynamodb-errors/dynamodb-query-condition-missed/,
// moto's query validation and floci-io/floci#3360 all carry it verbatim.
func validateKeyConditionSchema(hashAttrName, sortAttrName string, kc *keyCond) *protocol.AWSError {
	if kc == nil || kc.hashAttr == hashAttrName {
		return nil
	}
	if sc := kc.sortCond; sc != nil && sc.kind == sortKeyEq && sc.attr == hashAttrName && kc.hashAttr == sortAttrName {
		kc.hashAttr, sc.attr = sc.attr, kc.hashAttr
		kc.hashVal, sc.val = sc.val, kc.hashVal
		return nil
	}
	return errValidation("Query condition missed key schema element: " + hashAttrName)
}

// validateKeyConditionTypes checks every value a compiled
// KeyConditionExpression compares a key attribute against, so a Query cannot
// silently match nothing because it asked for a Number key with a String.
//
// hashAttrName and sortAttrName are the key attributes actually in play —
// the table's own for a base-table Query, the index's when IndexName is set.
// Either way their declared types live in table.AttributeDefinitions, which
// AWS requires to cover every key attribute of the table and of every index.
func validateKeyConditionTypes(table *Table, hashAttrName, sortAttrName string, kc *keyCond) *protocol.AWSError {
	if table == nil || kc == nil {
		return nil
	}
	if aerr := checkConditionValueType(table, hashAttrName, kc.hashVal); aerr != nil {
		return aerr
	}
	if kc.sortCond == nil {
		return nil
	}
	// val carries single-value conditions, lo/hi carry BETWEEN's bounds; the
	// unused ones are nil and checkConditionValueType skips them.
	for _, v := range [...]attrValue{kc.sortCond.val, kc.sortCond.lo, kc.sortCond.hi} {
		if aerr := checkConditionValueType(table, sortAttrName, v); aerr != nil {
			return aerr
		}
	}
	return nil
}

// checkConditionValueType is validateKeyConditionTypes' per-value step.
func checkConditionValueType(table *Table, attrName string, v attrValue) *protocol.AWSError {
	if v == nil {
		return nil
	}
	if _, _, mismatched := keyTypeMismatch(table, attrName, v); mismatched {
		return errValidation(invalidParameterPrefix + "Condition parameter type does not match schema type")
	}
	return nil
}

// keyTypeMismatch compares one supplied key-attribute value against the type
// AttributeDefinitions declares for that attribute, returning both types and
// whether they disagree. It is the single comparison both validators above
// build their (differently worded) AWS messages from.
//
// It reports no mismatch when the attribute has no declaration to check
// against, or when the value does not carry exactly one type tag: an empty or
// multi-tag AttributeValue is a malformed request AWS rejects with a message
// of its own that Overcast does not model, and no AWS SDK produces one.
func keyTypeMismatch(table *Table, attrName string, v attrValue) (declared, actual string, mismatched bool) {
	declared, ok := declaredKeyAttrType(table, attrName)
	if !ok {
		return "", "", false
	}
	if len(v) != 1 {
		return declared, "", false
	}
	actual = attrType(v)
	return declared, actual, actual != declared
}

// errKeyElementMismatch is AWS's answer whenever a supplied Key map disagrees
// with the key schema, whatever the disagreement is: the message names neither
// the attribute nor the kind of disagreement. See this file's header for the
// source.
func errKeyElementMismatch() *protocol.AWSError {
	return errValidation("The provided key element does not match the schema")
}
