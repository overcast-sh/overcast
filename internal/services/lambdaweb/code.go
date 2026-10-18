package lambdaweb

// code.go — the codeSource over the in-process S3 service, and the checks a
// revision's package must pass before it can be Active.

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/services/s3"
)

// maxPackageBytes is the documented 512 MB limit on a deployment package,
// unzipped (web-functions-code-s3.md, "Maximum deployment package size").
const maxPackageBytes int64 = 512 << 20

// s3Objects is the part of the S3 service the code source reads. *s3.Service
// satisfies it.
type s3Objects interface {
	BucketVersioning(ctx context.Context, bucket string) (string, *protocol.AWSError)
	HeadObjectVersion(ctx context.Context, bucket, key, versionID string) (s3.ObjectHead, *protocol.AWSError)
	GetObjectBytes(ctx context.Context, bucket, key, versionID string) ([]byte, *protocol.AWSError)
}

type s3CodeSource struct{ s3 s3Objects }

// NewS3CodeSource returns the codeSource that reads revision packages from the
// in-process S3 service. It holds no state: every call reads S3 as it is now,
// so a bucket or version deleted since the last call is seen at once.
func NewS3CodeSource(s3 s3Objects) codeSource { return &s3CodeSource{s3: s3} }

// Resolve implements codeSource. AWS's documentation says only that Lambda
// "rejects the request" when the bucket is not versioned or the object cannot
// be read; the error code (ValidationException) and every message below are
// inferred, not verified against real AWS.
func (c *s3CodeSource) Resolve(ctx context.Context, ref codeRef) (codeRef, int64, *protocol.AWSError) {
	status, aerr := c.s3.BucketVersioning(ctx, ref.Bucket)
	if aerr != nil {
		if aerr.Code == "NoSuchBucket" {
			return ref, 0, validationError("The S3 bucket %q does not exist.", ref.Bucket)
		}
		return ref, 0, aerr
	}
	// A suspended bucket is not "enabled": the documentation requires S3
	// versioning to be enabled (web-functions-code-s3.md, "S3 versioning").
	if status != "Enabled" {
		return ref, 0, validationError("The S3 bucket %q must have versioning enabled.", ref.Bucket)
	}
	head, aerr := c.s3.HeadObjectVersion(ctx, ref.Bucket, ref.Key, ref.VersionID)
	if aerr != nil {
		switch aerr.Code {
		case "NoSuchBucket", "NoSuchKey", "NoSuchVersion":
			return ref, 0, objectMissing(ref)
		}
		return ref, 0, aerr
	}
	if head.DeleteMarker {
		return ref, 0, objectMissing(ref)
	}
	if head.Size > maxPackageBytes {
		return ref, 0, validationError("The deployment package s3://%s/%s is %d bytes; the limit is 512 MB.", ref.Bucket, ref.Key, head.Size)
	}
	ref.VersionID = head.VersionID
	return ref, head.Size, nil
}

func objectMissing(ref codeRef) *protocol.AWSError {
	if ref.VersionID != "" {
		return validationError("The S3 object s3://%s/%s (version %s) does not exist.", ref.Bucket, ref.Key, ref.VersionID)
	}
	return validationError("The S3 object s3://%s/%s does not exist.", ref.Bucket, ref.Key)
}

// Fetch implements codeSource.
func (c *s3CodeSource) Fetch(ctx context.Context, ref codeRef) ([]byte, *protocol.AWSError) {
	return c.s3.GetObjectBytes(ctx, ref.Bucket, ref.Key, ref.VersionID)
}

// ─── Package validation ──────────────────────────────────────────────────────

// Error codes a Failed revision's errors[] carry. AWS documents the field but
// no code values, so these are Overcast's own (inferred).
const (
	codeInvalidPackage   = "InvalidCodePackage"  // not a readable zip
	codePackageTooLarge  = "CodePackageTooLarge" // unzipped size over 512 MB
	codeUnsafePath       = "InvalidCodePackage"  // an entry escapes the package root
	codeCodeUnavailable  = "CodeUnavailable"     // the S3 object cannot be read
	codeCodeAccessLost   = "CodeAccessLost"      // an environment could not fetch it later
	codeAttrS3Object     = "buildConfig.codeConfig.s3Object"
	reasonPackagePrefix  = "Lambda could not prepare the deployment package: "
	reasonCodeUnavailRsn = "Lambda could not read the deployment package from Amazon S3: "
)

// packageError is why a package was refused.
type packageError struct{ Code, Message string }

func (e *packageError) Error() string { return e.Code + ": " + e.Message }

// validatePackage checks that b is a readable zip whose entries stay inside the
// package root and unzip to at most maxPackageBytes. It reads every entry, so a
// corrupt member (bad CRC, truncated data) is found now and not when an
// environment starts, and it counts the bytes actually produced rather than
// trusting the sizes the archive declares.
func validatePackage(b []byte) *packageError {
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return &packageError{codeInvalidPackage, "The deployment package is not a valid .zip file: " + err.Error()}
	}
	var total int64
	for _, f := range zr.File {
		if !safeEntryName(f.Name) {
			return &packageError{codeUnsafePath, fmt.Sprintf("The deployment package contains the entry %q, which is outside the package root.", f.Name)}
		}
		if f.FileInfo().IsDir() {
			continue
		}
		if total += int64(f.UncompressedSize64); total > maxPackageBytes || f.UncompressedSize64 > uint64(maxPackageBytes) {
			return tooLarge()
		}
		rc, err := f.Open()
		if err != nil {
			return &packageError{codeInvalidPackage, fmt.Sprintf("The deployment package entry %q cannot be read: %v", f.Name, err)}
		}
		// Read one byte past the declared size so an entry that lies about it
		// is caught; the total budget is what remains of the limit.
		n, err := io.Copy(io.Discard, io.LimitReader(rc, int64(f.UncompressedSize64)+1))
		rc.Close()
		if err != nil {
			return &packageError{codeInvalidPackage, fmt.Sprintf("The deployment package entry %q is corrupt: %v", f.Name, err)}
		}
		if n != int64(f.UncompressedSize64) {
			return &packageError{codeInvalidPackage, fmt.Sprintf("The deployment package entry %q does not match its declared size.", f.Name)}
		}
	}
	return nil
}

func tooLarge() *packageError {
	return &packageError{codePackageTooLarge, "The deployment package is larger than 512 MB unzipped."}
}

// safeEntryName rejects absolute names, drive letters and any ".." element, in
// either slash style: the entry would unzip outside /var/task.
func safeEntryName(name string) bool {
	if name == "" {
		return false
	}
	n := strings.ReplaceAll(name, "\\", "/")
	if strings.HasPrefix(n, "/") || (len(n) >= 2 && n[1] == ':') {
		return false
	}
	for _, part := range strings.Split(n, "/") {
		if part == ".." {
			return false
		}
	}
	return path.Clean(n) != ".."
}
