package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"time"
)

// ManifestDigestField names the provenance entry that records the SHA-256 of
// the generated manifest. It lets a checkout verify offline that the committed
// manifest is the generator's own output, with no model corpus to hand.
const ManifestDigestField = "manifest-sha256"

// ManifestDigest returns the digest recorded for generated manifest contents.
func ManifestDigest(contents []byte) string {
	sum := sha256.Sum256(contents)
	return hex.EncodeToString(sum[:])
}

func updateModelVersion(path, revision, modelDate, manifestDigest, shapesDigest, sdkShapesDigest string) error {
	if strings.TrimSpace(revision) == "" {
		return fmt.Errorf("model revision must not be empty")
	}
	if _, err := time.Parse(time.DateOnly, modelDate); err != nil {
		return fmt.Errorf("model date %q: %w", modelDate, err)
	}
	if strings.TrimSpace(manifestDigest) == "" {
		return fmt.Errorf("manifest digest must not be empty")
	}
	if strings.TrimSpace(shapesDigest) == "" {
		return fmt.Errorf("shape snapshot digest must not be empty")
	}
	if strings.TrimSpace(sdkShapesDigest) == "" {
		return fmt.Errorf("SDK shape table digest must not be empty")
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read model version %s: %w", path, err)
	}
	lines := strings.Split(string(contents), "\n")
	revisionFound := replaceVersionField(lines, "revision", revision)
	dateFound := replaceVersionField(lines, "model-date", modelDate)
	digestFound := replaceVersionField(lines, ManifestDigestField, manifestDigest)
	shapesFound := replaceVersionField(lines, ShapesDigestField, shapesDigest)
	sdkShapesFound := replaceVersionField(lines, SDKShapesDigestField, sdkShapesDigest)
	if !revisionFound || !dateFound || !digestFound || !shapesFound || !sdkShapesFound {
		return fmt.Errorf("model version %s must contain revision, model-date, %s, %s and %s fields", path, ManifestDigestField, ShapesDigestField, SDKShapesDigestField)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		return fmt.Errorf("write model version %s: %w", path, err)
	}
	return nil
}

func replaceVersionField(lines []string, name, value string) bool {
	prefix := name + "="
	found := false
	for i, line := range lines {
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		if found {
			return false
		}
		lines[i] = prefix + value
		found = true
	}
	return found
}
