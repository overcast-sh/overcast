package awsshapes

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestLookup_decodesEveryGeneratedTable(t *testing.T) {
	for _, key := range Services() {
		// Given: a generated service table.
		// When: it is decoded.
		svc, ok, err := Lookup(key)

		// Then: it decodes, and every operation resolves to structure (or
		// Unit) input and output shapes a translator can walk.
		if err != nil || !ok {
			t.Fatalf("Lookup(%q) = ok %v, err %v", key, ok, err)
		}
		if len(svc.Protocols) == 0 {
			t.Errorf("%s: no protocol decoded", key)
		}
		operations := 0
		for _, shape := range svc.shapes {
			if shape.Kind != KindOperation {
				continue
			}
			operations++
			for _, io := range []*Shape{shape.Input, shape.Output} {
				if io != nil && io.Kind != KindStructure && io.Kind != KindUnit {
					t.Errorf("%s: operation %s has a %v input/output", key, shape.Name, io.Kind)
				}
			}
		}
		if operations == 0 {
			t.Errorf("%s: no operations decoded", key)
		}
	}
}

func TestLookup_s3Bindings(t *testing.T) {
	// Given: the S3 table.
	svc, ok, err := Lookup("s3")
	if err != nil || !ok {
		t.Fatalf("Lookup(s3) = %v, %v", ok, err)
	}

	// When: GetObject is resolved.
	op, found := svc.Operation("GetObject")
	if !found {
		t.Fatal("GetObject missing")
	}

	// Then: its HTTP binding and member traits came through.
	if op.Method != "GET" || op.URI != "/{Bucket}/{Key+}?x-id=GetObject" {
		t.Errorf("GetObject binding = %s %s", op.Method, op.URI)
	}
	metadata := op.Output.MemberNamed("Metadata")
	if metadata == nil || metadata.PrefixHeader == nil || *metadata.PrefixHeader != "x-amz-meta-" || metadata.Target.Kind != KindMap {
		t.Errorf("GetObjectOutput.Metadata = %+v", metadata)
	}
	body := op.Output.MemberNamed("Body")
	if body == nil || !body.Payload || !body.Target.Streaming || body.Target.Kind != KindBlob {
		t.Errorf("GetObjectOutput.Body = %+v", body)
	}
	if svc.XMLNamespace != "http://s3.amazonaws.com/doc/2006-03-01/" {
		t.Errorf("XMLNamespace = %q", svc.XMLNamespace)
	}
}

func TestLookup_listMemberXMLName(t *testing.T) {
	// Given: the EC2 table, whose lists name each element <item>.
	svc, _, err := Lookup("ec2")
	if err != nil {
		t.Fatal(err)
	}

	// When: DescribeInstances' output list is resolved.
	op, _ := svc.Operation("DescribeInstances")
	reservations := op.Output.MemberNamed("Reservations")

	// Then: the list member's @xmlName and the member's ec2QueryName survive.
	if reservations == nil || reservations.XMLName != "reservationSet" || reservations.Target.MemberXMLName != "item" {
		t.Errorf("Reservations = %+v target %+v", reservations, reservations.Target)
	}
	if filters := op.Input.MemberNamed("Filters"); filters == nil || filters.XMLName != "Filter" {
		t.Errorf("Filters = %+v", filters)
	}
}

func TestLookup_unknownService(t *testing.T) {
	// When: a service with no table is looked up.
	_, ok, err := Lookup("no-such-service")

	// Then: it is reported absent, not as an error.
	if ok || err != nil {
		t.Fatalf("Lookup = %v, %v", ok, err)
	}
}

// TestGeneratedTables_matchRecordedDigest is the offline provenance gate for
// the tables, as manifest_provenance_test.go is for the manifest: proving they
// match upstream needs the model checkout, but proving they are still the
// generator's own bytes does not.
func TestGeneratedTables_matchRecordedDigest(t *testing.T) {
	// Given: the committed tables and the provenance that describes them.
	files := generatedTables(t)
	recorded := versionField(t, "sdk-shapes-sha256")

	// When: they are digested as the generator digests them.
	lines := make([]string, 0, len(files))
	for name, contents := range files {
		sum := sha256.Sum256(contents)
		lines = append(lines, fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), name))
	}
	sort.Strings(lines)
	digest := sha256.Sum256([]byte(strings.Join(lines, "")))

	// Then: the digest matches.
	if got := hex.EncodeToString(digest[:]); got != recorded {
		t.Fatalf("internal/awsshapes/index.gen.go and tables/ do not match sdk-shapes-sha256 in models/aws/VERSION.\n"+
			"  recorded: %s\n  actual:   %s\n"+
			"Regenerate with `make generate-aws-operations` against the pinned AWS model checkout "+
			"rather than editing the generated files.", recorded, got)
	}
}

// maxSDKShapeTableBytes caps the committed text tables — repository weight,
// and what a model refresh asks a reviewer to read. Measured at 3,899,170
// bytes across 54 services at revision 8153df4c (EC2 alone is ~800 KB) and
// capped at 5 MiB, ~1.34x that, leaving room for model growth and a few more
// services.
const maxSDKShapeTableBytes = 5 * 1024 * 1024

// maxSDKShapePackedBytes caps what the tables cost every binary: their
// compressed copies in dist/, which are what the package embeds. Measured at
// 521,717 bytes at revision 8153df4c and capped at 1 MiB. Raising either cap
// is a reviewer's decision about size, never a reflex to turn a model refresh
// green.
const maxSDKShapePackedBytes = 1024 * 1024

func TestGeneratedTables_withinSizeBudget(t *testing.T) {
	// Given: the committed tables.
	files := generatedTables(t)

	// When: they are totalled as text and as the binary will carry them.
	text, packed := 0, 0
	for _, contents := range files {
		text += len(contents)
		p, err := Pack(contents)
		if err != nil {
			t.Fatal(err)
		}
		packed += len(p)
	}

	// Then: both totals are inside the reviewed budgets.
	if text > maxSDKShapeTableBytes {
		t.Errorf("internal/awsshapes/tables total %d bytes, over the %d-byte budget in maxSDKShapeTableBytes",
			text, maxSDKShapeTableBytes)
	}
	if packed > maxSDKShapePackedBytes {
		t.Errorf("the packed tables total %d bytes, over the %d-byte budget in maxSDKShapePackedBytes",
			packed, maxSDKShapePackedBytes)
	}
}

// generatedTables reads every generated file — the index and each text table —
// keyed by slash-separated path as cmd/awsmodelgen digests them.
func generatedTables(t *testing.T) map[string][]byte {
	t.Helper()
	files := make(map[string][]byte)
	index, err := os.ReadFile("index.gen.go")
	if err != nil {
		t.Fatal(err)
	}
	files["index.gen.go"] = index
	entries, err := os.ReadDir("tables")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".txt") {
			continue
		}
		contents, err := os.ReadFile(filepath.Join("tables", entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		files["tables/"+entry.Name()] = contents
	}
	if len(files) == 1 {
		t.Fatal("no generated tables found")
	}
	return files
}

func versionField(t *testing.T, name string) string {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join("..", "..", "models", "aws", "VERSION"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(contents), "\n") {
		if value, ok := strings.CutPrefix(strings.TrimSpace(line), name+"="); ok {
			return value
		}
	}
	t.Fatalf("models/aws/VERSION has no %s field", name)
	return ""
}
