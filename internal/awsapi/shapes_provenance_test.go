package awsapi

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The pruned Smithy shape snapshot (models/aws/shapes/) is the offline half of
// the same bargain manifest_provenance_test.go strikes for the manifest: the
// raw corpus is not vendored, so an ordinary pull request cannot prove the
// snapshot matches upstream — but it can prove the committed bytes are still
// exactly what the generator produced, that the snapshot has not quietly grown
// past its reviewed budget, and that nothing at runtime reads it.
//
// See docs/plans/inert-tier-rollout.md §4.6 and cmd/awsmodelgen/README.md.

// maxShapeSnapshotBytes caps the total size of the committed snapshot.
//
// Wave 2 (#1883) added secrets-manager, sns, kms and iam to the pilot four,
// bringing the committed snapshot to 9 services / 650,833 bytes — 2.6% of the
// 24 MiB fleet ceiling (inert-tier-rollout.md §4.6). The cap is raised to
// 800 KiB, ~1.26x that total, the same headroom factor §4.6 used for the
// original 336 KiB. s3 was also measured for this wave and excluded: at
// 2,200 bytes/op it is the only service over the 1,608 B/op gate, so it needs
// structural pruning or a compact encoding before it can join a wave.
//
// G6's eventbridge (#1116) then took it to 10 services / 734,034 bytes: 83,201
// bytes over 57 operations, 1,460 B/op, inside the same gate. It fits under the
// existing cap and does not raise it, leaving 85,166 bytes of headroom.
//
// Wave 1 of that phase ports three groups at once, and the other two services
// measure 61,721 bytes (kinesis) and 161,975 (cloudwatch-logs) at this
// revision. Only one further pair fits: eventbridge with kinesis, at 795,755.
// Every other combination exceeds the cap, so the port that lands after
// cloudwatch-logs — in whichever order they merge — is the one that has to
// argue for a larger one: as the reviewed scope decision below, not as the
// reflex that turns its own rebase green.
//
// Reviewed 2026-09-09, ahead of those two ports rather than inside either:
// the whole of G6 wave 1 is 734,034 + 161,975 + 61,721 = 957,730 bytes across
// 12 services, 3.8% of the 24 MiB fleet ceiling, every service inside the
// 1,608 B/op gate. The cap goes to 1,200 KiB — ~1.28x that total, the same
// headroom the two raises before it used — so both ports land on their
// merits. The next raise is a G6 cost as much as a G4 one: an authored
// scenario is model-checked, so porting a group of a service outside the
// corpus adds that service's snapshot (#1883, #1116).
//
// **Raise this constant deliberately, as a reviewer, never automatically.** It
// is the enforcement half of §4.6's size gate: growing it is how the fleet
// budget gets spent, and the projection that budget rests on is in §4.6. A
// failure here means the snapshot grew — decide whether the growth is scope
// (a service was added to models/aws/shapes-services.txt, which is a review
// decision) or encoding drift (which is a bug), and say which in the PR.
const maxShapeSnapshotBytes = 1200 * 1024

// shapeSnapshotDir is the committed snapshot, relative to this package.
var shapeSnapshotDir = filepath.Join("..", "..", "models", "aws", "shapes")

// TestCommittedShapeSnapshot_matchesRecordedDigest proves the committed
// snapshot is byte-for-byte the generator's own output, with no network and no
// model checkout — catching a hand-edit, a deletion, or a partial merge.
func TestCommittedShapeSnapshot_matchesRecordedDigest(t *testing.T) {
	// Given: the committed snapshot and the provenance that describes it.
	files := readShapeSnapshot(t)
	versionPath := filepath.Join("..", "..", "models", "aws", "VERSION")
	recorded, err := readVersionField(versionPath, "shapes-sha256")
	if err != nil {
		t.Fatalf("read model provenance: %v", err)
	}

	// When: the directory is digested as the generator digests it — sha256 over
	// the sorted "<file digest>  <relative path>" lines.
	lines := make([]string, 0, len(files))
	for name, contents := range files {
		sum := sha256.Sum256(contents)
		lines = append(lines, fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), name))
	}
	sort.Strings(lines)
	digest := sha256.Sum256([]byte(strings.Join(lines, "")))
	got := hex.EncodeToString(digest[:])

	// Then: it matches the digest recorded alongside the pinned revision.
	if got != recorded {
		t.Fatalf("models/aws/shapes does not match the digest recorded in %s.\n"+
			"  recorded: %s\n  actual:   %s\n"+
			"Regenerate with `make generate-aws-operations` against the pinned "+
			"AWS model checkout rather than editing the snapshot.",
			versionPath, recorded, got)
	}
}

// TestCommittedShapeSnapshot_withinSizeBudget enforces docs/plans/inert-tier-rollout.md
// §4.6's acceptance gate: the snapshot may only widen against a measured budget.
func TestCommittedShapeSnapshot_withinSizeBudget(t *testing.T) {
	// Given: the committed snapshot.
	files := readShapeSnapshot(t)

	// When: its files are totalled.
	total := 0
	for _, contents := range files {
		total += len(contents)
	}

	// Then: the total is within the reviewed budget.
	if total > maxShapeSnapshotBytes {
		t.Fatalf("models/aws/shapes totals %d bytes across %d services, over the "+
			"%d-byte budget in maxShapeSnapshotBytes.\n"+
			"Raising that constant is a reviewer's decision, not a fix: see "+
			"docs/plans/inert-tier-rollout.md §4.6 for the fleet projection it spends.",
			total, len(files), maxShapeSnapshotBytes)
	}
}

// TestShapeSnapshot_isGeneratorInputOnly holds the line §4.6 draws to reconcile
// the snapshot with aws-api-operation-coverage.md §3: the daemon never reads
// model data. Only build-time commands under cmd/ (and their tests) may name
// the snapshot; a reference from a runtime package would make model files a
// startup input, which is the thing that policy forbids.
func TestShapeSnapshot_isGeneratorInputOnly(t *testing.T) {
	// Given: the repository root, and the two ways Go code can name the path.
	root := filepath.Join("..", "..")
	joined := regexp.MustCompile(`"models"\s*,\s*"aws"\s*,\s*"shapes"`)

	// When: every non-test Go file outside cmd/ is scanned for the snapshot path.
	var offenders []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if entry.IsDir() {
			switch {
			case relative == ".":
				return nil
			case strings.HasPrefix(entry.Name(), "."),
				entry.Name() == "node_modules",
				relative == "cmd", relative == "scripts", relative == "web",
				relative == "compat", relative == "docs", relative == "models":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(relative, ".go") || strings.HasSuffix(relative, "_test.go") {
			return nil
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(contents), "models/aws/shapes") || joined.Match(contents) {
			offenders = append(offenders, relative)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan repository: %v", err)
	}

	// Then: nothing outside the build-time commands references it.
	if len(offenders) > 0 {
		sort.Strings(offenders)
		t.Fatalf("the pruned shape snapshot is generator input only, but these "+
			"non-test packages outside cmd/ reference it: %s\n"+
			"See docs/plans/aws-api-operation-coverage.md §3: runtime code must "+
			"never parse model data.", strings.Join(offenders, ", "))
	}
}

func readShapeSnapshot(t *testing.T) map[string][]byte {
	t.Helper()
	entries, err := os.ReadDir(shapeSnapshotDir)
	if err != nil {
		t.Fatalf("read shape snapshot directory: %v", err)
	}
	files := make(map[string][]byte, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		contents, err := os.ReadFile(filepath.Join(shapeSnapshotDir, entry.Name()))
		if err != nil {
			t.Fatalf("read shape snapshot %s: %v", entry.Name(), err)
		}
		files[entry.Name()] = contents
	}
	if len(files) == 0 {
		t.Fatal("models/aws/shapes holds no service snapshots")
	}
	return files
}
