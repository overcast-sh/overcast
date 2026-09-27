package iampolicy

import "testing"

func TestParseDocumentWithOptions_allowsKMSIneffectiveStatement(t *testing.T) {
	// Given: a resource-policy statement with no Action
	raw := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::000000000000:root"},"Resource":"*"}]}`
	src := SourceRef{ID: "key", Type: SourceTypeResourcePolicy}

	// Then: the ordinary IAM grammar still rejects it
	if _, err := ParseDocument(raw, src); err == nil {
		t.Fatal("ParseDocument accepted a statement with no Action")
	}

	// But: KMS can preserve AWS's accepted-but-ineffective statement behavior
	statements, err := ParseDocumentWithOptions(raw, src, ParseOptions{
		AllowMissingAction:  true,
		RequireVersion:      true,
		RequireStatements:   true,
		RequirePrincipal:    true,
		RejectEmptyElements: true,
	})
	if err != nil {
		t.Fatalf("ParseDocumentWithOptions returned error: %v", err)
	}
	if len(statements) != 1 || len(statements[0].Action) != 0 {
		t.Fatalf("statements = %#v, want one ineffective statement", statements)
	}
}

func TestParseDocumentWithOptions_requiresKMSPolicyStructure(t *testing.T) {
	opts := ParseOptions{
		AllowMissingAction:  true,
		AllowedVersions:     []string{"2008-10-17", "2012-10-17"},
		RequireStatements:   true,
		RequirePrincipal:    true,
		RejectEmptyElements: true,
	}
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{name: "unsupported version", raw: `{"Version":"2026-08-06","Statement":[{"Effect":"Allow","Principal":"*","Action":"kms:*"}]}`},
		{name: "statements", raw: `{"Version":"2012-10-17","Statement":[]}`},
		{name: "principal", raw: `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"kms:*"}]}`},
		{name: "action values", raw: `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":[]}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseDocumentWithOptions(tc.raw, SourceRef{ID: "key"}, opts); err == nil {
				t.Fatalf("accepted policy missing required %s", tc.name)
			}
		})
	}
}

func TestParseDocumentWithOptions_allowsAbsentVersionButRejectsBogusVersion(t *testing.T) {
	opts := ParseOptions{AllowedVersions: []string{"2008-10-17", "2012-10-17"}}
	withoutVersion := `{"Statement":[{"Effect":"Allow","Action":"kms:*","Resource":"*"}]}`
	if _, err := ParseDocumentWithOptions(withoutVersion, SourceRef{ID: "key"}, opts); err != nil {
		t.Fatalf("policy without Version returned error: %v", err)
	}
	bogusVersion := `{"Version":"2026-08-06","Statement":[{"Effect":"Allow","Action":"kms:*","Resource":"*"}]}`
	if _, err := ParseDocumentWithOptions(bogusVersion, SourceRef{ID: "key"}, opts); err == nil {
		t.Fatal("policy with bogus Version was accepted")
	}
}

func TestParseDocument_zeroOptionsStillAcceptUnknownVersion(t *testing.T) {
	raw := `{"Version":"custom-version","Statement":[{"Effect":"Allow","Principal":{"CanonicalUser":"79a59df900b949e55d96a1e698fbacedfd6e09d98eacf8f8d5218e7cd47ef2be"},"Action":"kms:*","Resource":"*"}]}`
	statements, err := ParseDocument(raw, SourceRef{ID: "resource-policy"})
	if err != nil {
		t.Fatalf("ParseDocument returned error: %v", err)
	}
	if len(statements) != 1 || len(statements[0].Principal.Unsupported) != 1 {
		t.Fatalf("statements = %#v, want unsupported principal preserved for evaluation", statements)
	}
}
