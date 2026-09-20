package dynamodb

import (
	"testing"
)

// ---------------------------------------------------------------------------
// KeyConditionExpression tests
// ---------------------------------------------------------------------------

func TestKeyCond_hashOnly(t *testing.T) {
	kc, err := compileKeyCondition(
		"pk = :pk",
		nil,
		map[string]attrValue{":pk": {"S": "user#1"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if kc.hashAttr != "pk" {
		t.Errorf("expected hashAttr 'pk', got %q", kc.hashAttr)
	}
	if extractScalar(kc.hashVal) != "user#1" {
		t.Errorf("expected hashVal 'user#1', got %q", extractScalar(kc.hashVal))
	}
	if kc.sortCond != nil {
		t.Error("expected no sort condition")
	}
}

func TestKeyCond_hashWithAlias(t *testing.T) {
	kc, err := compileKeyCondition(
		"#pk = :pk",
		map[string]string{"#pk": "partition_key"},
		map[string]attrValue{":pk": {"S": "abc"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if kc.hashAttr != "partition_key" {
		t.Errorf("expected hashAttr 'partition_key', got %q", kc.hashAttr)
	}
}

func TestKeyCond_hashAndSortEqual(t *testing.T) {
	kc, err := compileKeyCondition(
		"pk = :pk AND sk = :sk",
		nil,
		map[string]attrValue{
			":pk": {"S": "user#1"},
			":sk": {"S": "profile"},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if kc.sortCond == nil {
		t.Fatal("expected sort condition")
	}
	if kc.sortCond.attr != "sk" {
		t.Errorf("expected sort attr 'sk', got %q", kc.sortCond.attr)
	}
	if kc.sortCond.kind != sortKeyEq {
		t.Errorf("expected sortKeyEq, got %d", kc.sortCond.kind)
	}
}

func TestKeyCond_sortLessThan(t *testing.T) {
	kc, err := compileKeyCondition(
		"pk = :pk AND sk < :sk",
		nil,
		map[string]attrValue{
			":pk": {"S": "user#1"},
			":sk": {"S": "z"},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if kc.sortCond.kind != sortKeyLT {
		t.Errorf("expected sortKeyLT, got %d", kc.sortCond.kind)
	}
}

func TestKeyCond_sortLessThanOrEqual(t *testing.T) {
	kc, err := compileKeyCondition(
		"pk = :pk AND sk <= :sk",
		nil,
		map[string]attrValue{
			":pk": {"S": "u"},
			":sk": {"S": "z"},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if kc.sortCond.kind != sortKeyLE {
		t.Errorf("expected sortKeyLE, got %d", kc.sortCond.kind)
	}
}

func TestKeyCond_sortGreaterThan(t *testing.T) {
	kc, err := compileKeyCondition(
		"pk = :pk AND sk > :sk",
		nil,
		map[string]attrValue{
			":pk": {"S": "u"},
			":sk": {"S": "a"},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if kc.sortCond.kind != sortKeyGT {
		t.Errorf("expected sortKeyGT, got %d", kc.sortCond.kind)
	}
}

func TestKeyCond_sortGreaterThanOrEqual(t *testing.T) {
	kc, err := compileKeyCondition(
		"pk = :pk AND sk >= :sk",
		nil,
		map[string]attrValue{
			":pk": {"S": "u"},
			":sk": {"S": "a"},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if kc.sortCond.kind != sortKeyGE {
		t.Errorf("expected sortKeyGE, got %d", kc.sortCond.kind)
	}
}

func TestKeyCond_sortBetween(t *testing.T) {
	kc, err := compileKeyCondition(
		"pk = :pk AND sk BETWEEN :lo AND :hi",
		nil,
		map[string]attrValue{
			":pk": {"S": "user#1"},
			":lo": {"S": "a"},
			":hi": {"S": "z"},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if kc.sortCond.kind != sortKeyBetween {
		t.Errorf("expected sortKeyBetween, got %d", kc.sortCond.kind)
	}
	if extractScalar(kc.sortCond.lo) != "a" {
		t.Errorf("expected lo 'a', got %q", extractScalar(kc.sortCond.lo))
	}
	if extractScalar(kc.sortCond.hi) != "z" {
		t.Errorf("expected hi 'z', got %q", extractScalar(kc.sortCond.hi))
	}
}

func TestKeyCond_sortBeginsWith(t *testing.T) {
	kc, err := compileKeyCondition(
		"pk = :pk AND begins_with(sk, :prefix)",
		nil,
		map[string]attrValue{
			":pk":     {"S": "user#1"},
			":prefix": {"S": "order#"},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if kc.sortCond.kind != sortKeyBeginsWith {
		t.Errorf("expected sortKeyBeginsWith, got %d", kc.sortCond.kind)
	}
}

// ---------------------------------------------------------------------------
// sortKeyCond.matchItem tests
// ---------------------------------------------------------------------------

func TestSortKeyCond_matchEqual(t *testing.T) {
	cond := &sortKeyCond{
		attr: "sk",
		kind: sortKeyEq,
		val:  attrValue{"S": "profile"},
	}
	item := Item{"sk": attrValue{"S": "profile"}}
	if !cond.matchItem(item) {
		t.Error("expected match for equal sort key")
	}

	item2 := Item{"sk": attrValue{"S": "other"}}
	if cond.matchItem(item2) {
		t.Error("expected no match for different sort key")
	}
}

func TestSortKeyCond_matchBetween(t *testing.T) {
	cond := &sortKeyCond{
		attr: "sk",
		kind: sortKeyBetween,
		lo:   attrValue{"N": "10"},
		hi:   attrValue{"N": "20"},
	}
	tests := []struct {
		val  string
		want bool
	}{
		{"5", false},
		{"10", true},
		{"15", true},
		{"20", true},
		{"25", false},
	}
	for _, tc := range tests {
		item := Item{"sk": attrValue{"N": tc.val}}
		got := cond.matchItem(item)
		if got != tc.want {
			t.Errorf("matchItem(sk=%s): got %v, want %v", tc.val, got, tc.want)
		}
	}
}

func TestSortKeyCond_matchBeginsWith(t *testing.T) {
	cond := &sortKeyCond{
		attr: "sk",
		kind: sortKeyBeginsWith,
		val:  attrValue{"S": "order#"},
	}
	item1 := Item{"sk": attrValue{"S": "order#123"}}
	if !cond.matchItem(item1) {
		t.Error("expected match for begins_with")
	}

	item2 := Item{"sk": attrValue{"S": "user#1"}}
	if cond.matchItem(item2) {
		t.Error("expected no match for different prefix")
	}
}

func TestSortKeyCond_matchMissingAttribute(t *testing.T) {
	cond := &sortKeyCond{
		attr: "sk",
		kind: sortKeyEq,
		val:  attrValue{"S": "x"},
	}
	item := Item{} // no sk attribute
	if cond.matchItem(item) {
		t.Error("expected no match when attribute missing")
	}
}

func TestSortKeyCond_matchLessThan(t *testing.T) {
	cond := &sortKeyCond{
		attr: "sk",
		kind: sortKeyLT,
		val:  attrValue{"N": "10"},
	}
	if !cond.matchItem(Item{"sk": attrValue{"N": "5"}}) {
		t.Error("expected 5 < 10")
	}
	if cond.matchItem(Item{"sk": attrValue{"N": "10"}}) {
		t.Error("expected 10 not < 10")
	}
	if cond.matchItem(Item{"sk": attrValue{"N": "15"}}) {
		t.Error("expected 15 not < 10")
	}
}

func TestSortKeyCond_matchGreaterThanOrEqual(t *testing.T) {
	cond := &sortKeyCond{
		attr: "sk",
		kind: sortKeyGE,
		val:  attrValue{"N": "10"},
	}
	if cond.matchItem(Item{"sk": attrValue{"N": "5"}}) {
		t.Error("expected 5 not >= 10")
	}
	if !cond.matchItem(Item{"sk": attrValue{"N": "10"}}) {
		t.Error("expected 10 >= 10")
	}
	if !cond.matchItem(Item{"sk": attrValue{"N": "15"}}) {
		t.Error("expected 15 >= 10")
	}
}

// ---------------------------------------------------------------------------
// Compilation errors
// ---------------------------------------------------------------------------

func TestKeyCond_nonSimpleHashKey(t *testing.T) {
	_, err := compileKeyCondition(
		"a.b = :pk",
		nil,
		map[string]attrValue{":pk": {"S": "x"}},
	)
	if err == nil {
		t.Error("expected error for nested hash key path")
	}
}

func TestKeyCond_missingHashPlaceholder(t *testing.T) {
	_, err := compileKeyCondition(
		"pk = not_a_placeholder",
		nil,
		nil,
	)
	if err == nil {
		t.Error("expected error when hash value is not a placeholder")
	}
}

func TestKeyCond_trailingTokens(t *testing.T) {
	_, err := compileKeyCondition(
		"pk = :pk extra",
		nil,
		map[string]attrValue{":pk": {"S": "x"}},
	)
	if err == nil {
		t.Error("expected error for trailing tokens")
	}
}

func TestKeyCond_sortKeyNotSimple(t *testing.T) {
	_, err := compileKeyCondition(
		"pk = :pk AND a.b = :sk",
		nil,
		map[string]attrValue{
			":pk": {"S": "x"},
			":sk": {"S": "y"},
		},
	)
	if err == nil {
		t.Error("expected error for nested sort key path")
	}
}

// ---------------------------------------------------------------------------
// Either-order parsing and one-condition-per-key (issue #135)
// ---------------------------------------------------------------------------

// The partition-key condition is the equality, wherever it is written. Role
// assignment is the parser's alone except when both terms are equalities, so
// these cases must come out of compileKeyCondition already canonical.
func TestKeyCond_termsInEitherOrder(t *testing.T) {
	values := map[string]attrValue{
		":pk": {"S": "user#1"},
		":sk": {"S": "a"},
		":hi": {"S": "z"},
	}
	cases := []struct {
		name     string
		expr     string
		wantSort sortKeyCondKind
	}{
		{"greaterThanFirst", "sk > :sk AND pk = :pk", sortKeyGT},
		{"greaterOrEqualFirst", "sk >= :sk AND pk = :pk", sortKeyGE},
		{"lessThanFirst", "sk < :sk AND pk = :pk", sortKeyLT},
		{"lessOrEqualFirst", "sk <= :sk AND pk = :pk", sortKeyLE},
		{"beginsWithFirst", "begins_with(sk, :sk) AND pk = :pk", sortKeyBeginsWith},
		{"betweenFirst", "sk BETWEEN :sk AND :hi AND pk = :pk", sortKeyBetween},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kc, err := compileKeyCondition(tc.expr, nil, values)
			if err != nil {
				t.Fatal(err)
			}
			if kc.hashAttr != "pk" {
				t.Errorf("hashAttr = %q, want %q", kc.hashAttr, "pk")
			}
			if extractScalar(kc.hashVal) != "user#1" {
				t.Errorf("hashVal = %q, want %q", extractScalar(kc.hashVal), "user#1")
			}
			if kc.sortCond == nil {
				t.Fatal("expected a sort condition")
			}
			if kc.sortCond.attr != "sk" {
				t.Errorf("sort attr = %q, want %q", kc.sortCond.attr, "sk")
			}
			if kc.sortCond.kind != tc.wantSort {
				t.Errorf("sort kind = %d, want %d", kc.sortCond.kind, tc.wantSort)
			}
		})
	}
}

// With no equality anywhere, no term can be the partition-key condition. The
// parser says so with an empty hashAttr rather than a message of its own,
// because only validateKeyConditionSchema knows the partition key's name.
func TestKeyCond_noEqualityLeavesHashUnassigned(t *testing.T) {
	kc, err := compileKeyCondition(
		"sk > :sk",
		nil,
		map[string]attrValue{":sk": {"S": "a"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if kc.hashAttr != "" {
		t.Errorf("hashAttr = %q, want empty", kc.hashAttr)
	}
	if kc.sortCond == nil || kc.sortCond.attr != "sk" {
		t.Errorf("sort condition = %+v, want one on sk", kc.sortCond)
	}
}

func TestKeyCond_twoConditionsOnOneAttribute(t *testing.T) {
	values := map[string]attrValue{
		":a": {"S": "a"},
		":b": {"S": "b"},
	}
	for _, expr := range []string{
		"pk = :a AND pk = :b",
		"pk = :a AND begins_with(pk, :b)",
		"sk >= :a AND sk <= :b",
		"sk BETWEEN :a AND :b AND sk = :a",
	} {
		t.Run(expr, func(t *testing.T) {
			_, err := compileKeyCondition(expr, nil, values)
			if err == nil {
				t.Fatalf("compileKeyCondition(%q) accepted, want rejected", expr)
			}
			if err.Error() != msgOneConditionPerKey {
				t.Errorf("error = %q, want %q", err.Error(), msgOneConditionPerKey)
			}
		})
	}
}
