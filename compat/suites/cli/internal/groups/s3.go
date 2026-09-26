package groups

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/overcast-sh/overcast-compat-cli/internal/awscli"
	"github.com/overcast-sh/overcast-compat-cli/internal/harness"
)

// S3 returns the S3 service group.
func S3() ServiceGroup {
	g := &s3Group{}
	return ServiceGroup{
		Impls: map[string]harness.TestFn{
			// s3-crud
			"s3-crud:CreateBucket":           g.CreateBucket,
			"s3-crud:PutObject":              g.PutObject,
			"s3-crud:HeadObject":             g.HeadObject,
			"s3-crud:GetObject":              g.GetObject,
			"s3-crud:ListObjectsV2":          g.ListObjectsV2,
			"s3-crud:PutObjectMultipleKeys":  g.PutObjectMultipleKeys,
			"s3-crud:ListObjectsV2Delimiter": g.ListObjectsV2Delimiter,

			"s3-crud:PutObjectFormContentType": g.PutObjectFormContentType,
			"s3-crud:PutObjectPlusInKey":       g.PutObjectPlusInKey,

			"s3-crud:DeleteObject":  g.DeleteObject,
			"s3-crud:DeleteObjects": g.DeleteObjects,
			"s3-crud:DeleteBucket":  g.DeleteBucket,
			// s3-copy
			"s3-copy:CreateSourceBucket": g.CreateSourceBucket,
			"s3-copy:PutSourceObject":    g.PutSourceObject,
			"s3-copy:CopyObject":         g.CopyObject,
			// s3-multipart
			"s3-multipart:CreateMultipartUpload":   g.CreateMultipartUpload,
			"s3-multipart:UploadPart":              g.UploadPart,
			"s3-multipart:CompleteMultipartUpload": g.CompleteMultipartUpload,
			"s3-multipart:AbortMultipartUpload":    g.AbortMultipartUpload,
			// s3-versioning
			"s3-versioning:PutBucketVersioning": g.PutBucketVersioning,
			"s3-versioning:GetBucketVersioning": g.GetBucketVersioning,
			// s3-tagging
			"s3-tagging:PutObjectTagging": g.PutObjectTagging,
			"s3-tagging:GetObjectTagging": g.GetObjectTagging,
			"s3-tagging:PutBucketTagging": g.PutBucketTagging,
			"s3-tagging:GetBucketTagging": g.GetBucketTagging,
			// s3-website
			"s3-website:PutBucketWebsite": g.PutBucketWebsite,
			"s3-website:GetBucketWebsite": g.GetBucketWebsite,
			// s3-cors
			"s3-cors:PutBucketCors": g.PutBucketCors,
			"s3-cors:GetBucketCors": g.GetBucketCors,
			// s3-lifecycle
			"s3-lifecycle:PutBucketLifecycleConfiguration":            g.PutBucketLifecycleConfiguration,
			"s3-lifecycle:GetBucketLifecycleConfiguration":            g.GetBucketLifecycleConfiguration,
			"s3-lifecycle:DeleteBucketLifecycle":                      g.DeleteBucketLifecycle,
			"s3-lifecycle:GetBucketLifecycleConfigurationAfterDelete": g.GetBucketLifecycleConfigurationAfterDelete,
		},
		Setup: map[string]func(context.Context, *harness.TestContext) error{
			"s3-crud":       g.setupCrud,
			"s3-copy":       g.setupCopy,
			"s3-multipart":  g.setupMultipart,
			"s3-versioning": g.setupVersioning,
			"s3-tagging":    g.setupTagging,
			"s3-website":    g.setupWebsite,
			"s3-cors":       g.setupCors,
			"s3-lifecycle":  g.setupLifecycle,
		},
		Teardown: map[string]func(context.Context, *harness.TestContext) error{
			"s3-crud":       g.teardownBucket,
			"s3-copy":       g.teardownCopy,
			"s3-multipart":  g.teardownMultipart,
			"s3-versioning": g.teardownVersioning,
			"s3-tagging":    g.teardownBucket,
			"s3-website":    g.teardownBucket,
			"s3-cors":       g.teardownBucket,
			"s3-lifecycle":  g.teardownLifecycle,
		},
	}
}

// One Namer per S3 sub-group ensures parallel groups never share bucket names.
var (
	s3CrudNamer    = harness.NewNamer("s3-crud")
	s3CopyNamer    = harness.NewNamer("s3-copy")
	s3CopySrcNamer = harness.NewNamer("s3-copy-src")
	s3MpNamer      = harness.NewNamer("s3-mp")
	s3VerNamer     = harness.NewNamer("s3-ver")
	s3TagNamer     = harness.NewNamer("s3-tag")
	s3WebNamer     = harness.NewNamer("s3-web")
	s3CorsNamer    = harness.NewNamer("s3-cors")
	s3LcNamer      = harness.NewNamer("s3-lc")
)

type s3Group struct{}

// bucket returns the bucket name for the current test group, stored in context
// by each group's setup function to avoid name collisions between parallel groups.
func (g *s3Group) bucket(t *harness.TestContext) string {
	if b := t.GetString("bucket"); b != "" {
		return b
	}
	return s3CrudNamer.Name(t)
}

func (g *s3Group) srcBucket(t *harness.TestContext) string {
	return s3CopySrcNamer.Name(t)
}

// ─── s3-crud ─────────────────────────────────────────────────────────────────

func (g *s3Group) setupCrud(_ context.Context, t *harness.TestContext) error {
	t.Set("bucket", s3CrudNamer.Name(t))
	return nil
}

func (g *s3Group) setupCopy(_ context.Context, t *harness.TestContext) error {
	t.Set("bucket", s3CopyNamer.Name(t))
	return nil
}

func (g *s3Group) CreateBucket(_ context.Context, t *harness.TestContext) error {
	return awscli.Run(t.Endpoint, t.Region, "s3api", "create-bucket", "--bucket", g.bucket(t))
}

func (g *s3Group) PutObject(_ context.Context, t *harness.TestContext) error {
	if err := awscli.RunWithStdin(t.Endpoint, t.Region, "hello",
		"s3api", "put-object",
		"--bucket", g.bucket(t),
		"--key", "hello.txt",
		"--content-type", "text/plain",
		"--body", "/dev/stdin",
	); err != nil {
		return err
	}
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"s3api", "head-object",
		"--bucket", g.bucket(t),
		"--key", "hello.txt",
	)
	if err != nil {
		return fmt.Errorf("s3 PutObject: head-object failed: %w", err)
	}
	if out["ContentLength"] == nil {
		return fmt.Errorf("s3 PutObject: missing ContentLength")
	}
	return nil
}

func (g *s3Group) HeadObject(_ context.Context, t *harness.TestContext) error {
	_, err := awscli.RunOutput(t.Endpoint, t.Region,
		"s3api", "head-object",
		"--bucket", g.bucket(t),
		"--key", "hello.txt",
	)
	return err
}

func (g *s3Group) GetObject(_ context.Context, t *harness.TestContext) error {
	return awscli.Run(t.Endpoint, t.Region,
		"s3api", "get-object",
		"--bucket", g.bucket(t),
		"--key", "hello.txt",
		"/dev/null",
	)
}

func (g *s3Group) ListObjectsV2(_ context.Context, t *harness.TestContext) error {
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"s3api", "list-objects-v2",
		"--bucket", g.bucket(t),
	)
	if err != nil {
		return err
	}
	if out["Contents"] == nil {
		return fmt.Errorf("s3 ListObjectsV2: expected Contents, got none")
	}
	return nil
}

func (g *s3Group) PutObjectMultipleKeys(_ context.Context, t *harness.TestContext) error {
	for _, key := range []string{"prefix/a.txt", "prefix/b.txt"} {
		if err := awscli.RunWithStdin(t.Endpoint, t.Region, "data",
			"s3api", "put-object",
			"--bucket", g.bucket(t),
			"--key", key,
			"--body", "/dev/stdin",
		); err != nil {
			return err
		}
	}
	return nil
}

func (g *s3Group) ListObjectsV2Delimiter(_ context.Context, t *harness.TestContext) error {
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"s3api", "list-objects-v2",
		"--bucket", g.bucket(t),
		"--delimiter", "/",
		"--prefix", "prefix/",
	)
	if err != nil {
		return err
	}
	prefixes, _ := out["CommonPrefixes"].([]any)
	contents, _ := out["Contents"].([]any)
	if len(prefixes) == 0 && len(contents) == 0 {
		return fmt.Errorf("s3 ListObjectsV2Delimiter: expected CommonPrefixes or Contents, got none")
	}
	return nil
}

// getObjectBody downloads a key to a temp file and returns its contents. The
// CLI writes GetObject output to a path rather than stdout, so reading a body
// back needs a file the test then cleans up.
func (g *s3Group) getObjectBody(t *harness.TestContext, key string) ([]byte, error) {
	f, err := os.CreateTemp("", "s3-body-*")
	if err != nil {
		return nil, err
	}
	path := f.Name()
	f.Close()             //nolint:errcheck
	defer os.Remove(path) //nolint:errcheck

	if err := awscli.Run(t.Endpoint, t.Region,
		"s3api", "get-object",
		"--bucket", g.bucket(t),
		"--key", key,
		path,
	); err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

// PutObjectFormContentType writes an object whose Content-Type is the one a bare
// HTTP client sends when none is set. Content-Type is metadata on AWS, not a
// parsing instruction, but an emulator that sniffs it to spot AWS Query traffic
// can consume the body before the S3 handler sees it — silently storing zero
// bytes. The CLI picks a sensible type on its own, so only an explicit
// --content-type reaches the case.
func (g *s3Group) PutObjectFormContentType(_ context.Context, t *harness.TestContext) error {
	const key = "form-content-type"
	const body = "hello-body-check"

	if err := awscli.RunWithStdin(t.Endpoint, t.Region, body,
		"s3api", "put-object",
		"--bucket", g.bucket(t),
		"--key", key,
		"--content-type", "application/x-www-form-urlencoded",
		"--body", "/dev/stdin",
	); err != nil {
		return err
	}

	stored, err := g.getObjectBody(t, key)
	if err != nil {
		return fmt.Errorf("s3 PutObjectFormContentType: get-object failed: %w", err)
	}
	if string(stored) != body {
		return fmt.Errorf("s3 PutObjectFormContentType: expected body %q, got %q", body, string(stored))
	}

	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"s3api", "head-object",
		"--bucket", g.bucket(t),
		"--key", key,
	)
	if err != nil {
		return fmt.Errorf("s3 PutObjectFormContentType: head-object failed: %w", err)
	}
	if ct, _ := out["ContentType"].(string); ct != "application/x-www-form-urlencoded" {
		return fmt.Errorf("s3 PutObjectFormContentType: expected ContentType %q, got %q",
			"application/x-www-form-urlencoded", ct)
	}
	// Leave the group bucket as it was found: DeleteBucket below deletes this
	// shared bucket and needs it empty by then.
	return g.deleteCrudKey(t, key)
}

// deleteCrudKey removes a key a test added to the shared s3-crud bucket, so
// the group's later DeleteBucket still sees an empty bucket.
func (g *s3Group) deleteCrudKey(t *harness.TestContext, key string) error {
	if err := awscli.Run(t.Endpoint, t.Region,
		"s3api", "delete-object",
		"--bucket", g.bucket(t),
		"--key", key,
	); err != nil {
		return fmt.Errorf("s3 cleanup: delete-object %q: %w", key, err)
	}
	return nil
}

// PutObjectPlusInKey writes an object whose key contains "+". The CLI
// percent-encodes it as %2B in the request path — unlike a space or a
// multi-byte character, whose encodings survive a round trip through a server's
// URL canonicalisation — so a server that reads the raw path without decoding
// stores the literal three characters "%2B" as part of the key.
func (g *s3Group) PutObjectPlusInKey(_ context.Context, t *harness.TestContext) error {
	const key = "plusonly/a+b.txt"
	const body = "plus-body"

	if err := awscli.RunWithStdin(t.Endpoint, t.Region, body,
		"s3api", "put-object",
		"--bucket", g.bucket(t),
		"--key", key,
		"--body", "/dev/stdin",
	); err != nil {
		return err
	}

	stored, err := g.getObjectBody(t, key)
	if err != nil {
		return fmt.Errorf("s3 PutObjectPlusInKey: get-object failed: %w", err)
	}
	if string(stored) != body {
		return fmt.Errorf("s3 PutObjectPlusInKey: expected body %q, got %q", body, string(stored))
	}

	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"s3api", "list-objects-v2",
		"--bucket", g.bucket(t),
		"--prefix", "plusonly/",
	)
	if err != nil {
		return fmt.Errorf("s3 PutObjectPlusInKey: list-objects-v2 failed: %w", err)
	}
	contents, _ := out["Contents"].([]any)
	keys := make([]string, 0, len(contents))
	found := false
	for _, entry := range contents {
		obj, _ := entry.(map[string]any)
		k, _ := obj["Key"].(string)
		keys = append(keys, k)
		if k == key {
			found = true
		}
	}
	if !found {
		return fmt.Errorf("s3 PutObjectPlusInKey: expected key %q in list-objects-v2, got %v", key, keys)
	}
	// Leave the group bucket as it was found: DeleteBucket below deletes this
	// shared bucket and needs it empty by then.
	return g.deleteCrudKey(t, key)
}

func (g *s3Group) DeleteObject(_ context.Context, t *harness.TestContext) error {
	if err := awscli.Run(t.Endpoint, t.Region,
		"s3api", "delete-object",
		"--bucket", g.bucket(t),
		"--key", "hello.txt",
	); err != nil {
		return err
	}
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"s3api", "list-objects-v2",
		"--bucket", g.bucket(t),
	)
	if err != nil {
		return fmt.Errorf("s3 DeleteObject: list-objects-v2 failed: %w", err)
	}
	contents, _ := out["Contents"].([]any)
	for _, raw := range contents {
		if m, ok := raw.(map[string]any); ok && m["Key"] == "hello.txt" {
			return fmt.Errorf("s3 DeleteObject: key 'hello.txt' still present after delete")
		}
	}
	return nil
}

func (g *s3Group) DeleteObjects(_ context.Context, t *harness.TestContext) error {
	if err := awscli.Run(t.Endpoint, t.Region,
		"s3api", "delete-objects",
		"--bucket", g.bucket(t),
		"--delete", `{"Objects":[{"Key":"prefix/a.txt"},{"Key":"prefix/b.txt"}],"Quiet":true}`,
	); err != nil {
		return err
	}
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"s3api", "list-objects-v2",
		"--bucket", g.bucket(t),
	)
	if err != nil {
		return fmt.Errorf("s3 DeleteObjects: list-objects-v2 failed: %w", err)
	}
	contents, _ := out["Contents"].([]any)
	for _, raw := range contents {
		if m, ok := raw.(map[string]any); ok {
			if m["Key"] == "prefix/a.txt" || m["Key"] == "prefix/b.txt" {
				return fmt.Errorf("s3 DeleteObjects: key %v still present after delete", m["Key"])
			}
		}
	}
	return nil
}

func (g *s3Group) DeleteBucket(_ context.Context, t *harness.TestContext) error {
	bucket := g.bucket(t)
	if err := awscli.Run(t.Endpoint, t.Region,
		"s3api", "delete-bucket",
		"--bucket", bucket,
	); err != nil {
		return err
	}
	out, err := awscli.RunOutput(t.Endpoint, t.Region, "s3api", "list-buckets")
	if err != nil {
		return fmt.Errorf("s3 DeleteBucket: list-buckets failed: %w", err)
	}
	buckets, _ := out["Buckets"].([]any)
	for _, raw := range buckets {
		if m, ok := raw.(map[string]any); ok && m["Name"] == bucket {
			return fmt.Errorf("s3 DeleteBucket: bucket %q still present after delete", bucket)
		}
	}
	return nil
}

func (g *s3Group) teardownBucket(_ context.Context, t *harness.TestContext) error {
	// Best-effort: purge objects then delete bucket.
	out, _ := awscli.RunOutput(t.Endpoint, t.Region,
		"s3api", "list-objects-v2", "--bucket", g.bucket(t),
	)
	if out != nil {
		if contents, ok := out["Contents"].([]any); ok {
			for _, item := range contents {
				obj := item.(map[string]any)
				if key, ok := obj["Key"].(string); ok {
					awscli.Run(t.Endpoint, t.Region, "s3api", "delete-object", //nolint:errcheck
						"--bucket", g.bucket(t), "--key", key)
				}
			}
		}
	}
	awscli.Run(t.Endpoint, t.Region, "s3api", "delete-bucket", "--bucket", g.bucket(t)) //nolint:errcheck
	return nil
}

// teardownMultipart aborts any incomplete multipart uploads before deleting the bucket.
func (g *s3Group) teardownMultipart(ctx context.Context, t *harness.TestContext) error {
	out, _ := awscli.RunOutput(t.Endpoint, t.Region,
		"s3api", "list-multipart-uploads", "--bucket", g.bucket(t),
	)
	if out != nil {
		if uploads, ok := out["Uploads"].([]any); ok {
			for _, u := range uploads {
				up := u.(map[string]any)
				key, _ := up["Key"].(string)
				uploadID, _ := up["UploadId"].(string)
				if key != "" && uploadID != "" {
					awscli.Run(t.Endpoint, t.Region, "s3api", "abort-multipart-upload", //nolint:errcheck
						"--bucket", g.bucket(t), "--key", key, "--upload-id", uploadID)
				}
			}
		}
	}
	return g.teardownBucket(ctx, t)
}

// teardownVersioning deletes all object versions and delete markers before deleting the bucket.
func (g *s3Group) teardownVersioning(_ context.Context, t *harness.TestContext) error {
	out, _ := awscli.RunOutput(t.Endpoint, t.Region,
		"s3api", "list-object-versions", "--bucket", g.bucket(t),
	)
	if out != nil {
		for _, listKey := range []string{"Versions", "DeleteMarkers"} {
			if items, ok := out[listKey].([]any); ok {
				for _, item := range items {
					obj := item.(map[string]any)
					key, _ := obj["Key"].(string)
					versionID, _ := obj["VersionId"].(string)
					if key != "" && versionID != "" {
						awscli.Run(t.Endpoint, t.Region, "s3api", "delete-object", //nolint:errcheck
							"--bucket", g.bucket(t), "--key", key, "--version-id", versionID)
					}
				}
			}
		}
	}
	awscli.Run(t.Endpoint, t.Region, "s3api", "delete-bucket", "--bucket", g.bucket(t)) //nolint:errcheck
	return nil
}

// ─── s3-copy ─────────────────────────────────────────────────────────────────

func (g *s3Group) CreateSourceBucket(_ context.Context, t *harness.TestContext) error {
	return awscli.Run(t.Endpoint, t.Region, "s3api", "create-bucket", "--bucket", g.srcBucket(t))
}

func (g *s3Group) PutSourceObject(_ context.Context, t *harness.TestContext) error {
	if err := awscli.RunWithStdin(t.Endpoint, t.Region, "source content",
		"s3api", "put-object",
		"--bucket", g.srcBucket(t),
		"--key", "src.txt",
		"--body", "/dev/stdin",
	); err != nil {
		return err
	}
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"s3api", "head-object",
		"--bucket", g.srcBucket(t),
		"--key", "src.txt",
	)
	if err != nil {
		return fmt.Errorf("s3 PutSourceObject: head-object failed: %w", err)
	}
	if out["ContentLength"] == nil {
		return fmt.Errorf("s3 PutSourceObject: missing ContentLength")
	}
	return nil
}

func (g *s3Group) CopyObject(_ context.Context, t *harness.TestContext) error {
	// Destination bucket is created here (lazily) for the copy group.
	if err := awscli.Run(t.Endpoint, t.Region, "s3api", "create-bucket", "--bucket", g.bucket(t)); err != nil && !isAlreadyExists(err) {
		return err
	}
	if err := awscli.Run(t.Endpoint, t.Region,
		"s3api", "copy-object",
		"--bucket", g.bucket(t),
		"--key", "dst.txt",
		"--copy-source", fmt.Sprintf("%s/src.txt", g.srcBucket(t)),
	); err != nil {
		return err
	}
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"s3api", "head-object",
		"--bucket", g.bucket(t),
		"--key", "dst.txt",
	)
	if err != nil {
		return fmt.Errorf("s3 CopyObject: head-object on dst.txt failed: %w", err)
	}
	if out["ContentLength"] == nil {
		return fmt.Errorf("s3 CopyObject: missing ContentLength on copied object")
	}
	return nil
}

func (g *s3Group) teardownCopy(_ context.Context, t *harness.TestContext) error {
	for _, b := range []string{g.bucket(t), g.srcBucket(t)} {
		out, _ := awscli.RunOutput(t.Endpoint, t.Region, "s3api", "list-objects-v2", "--bucket", b)
		if out != nil {
			if contents, ok := out["Contents"].([]any); ok {
				for _, item := range contents {
					obj := item.(map[string]any)
					if key, ok := obj["Key"].(string); ok {
						awscli.Run(t.Endpoint, t.Region, "s3api", "delete-object", "--bucket", b, "--key", key) //nolint:errcheck
					}
				}
			}
		}
		awscli.Run(t.Endpoint, t.Region, "s3api", "delete-bucket", "--bucket", b) //nolint:errcheck
	}
	return nil
}

// ─── s3-multipart ────────────────────────────────────────────────────────────

func (g *s3Group) setupMultipart(_ context.Context, t *harness.TestContext) error {
	t.Set("bucket", s3MpNamer.Name(t))
	if err := awscli.Run(t.Endpoint, t.Region, "s3api", "create-bucket", "--bucket", g.bucket(t)); err != nil && !isAlreadyExists(err) {
		return err
	}
	return nil
}

func (g *s3Group) CreateMultipartUpload(_ context.Context, t *harness.TestContext) error {
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"s3api", "create-multipart-upload",
		"--bucket", g.bucket(t),
		"--key", "multipart.bin",
	)
	if err != nil {
		return err
	}
	uploadID, _ := out["UploadId"].(string)
	if uploadID == "" {
		return fmt.Errorf("s3 CreateMultipartUpload: missing UploadId")
	}
	t.Set("upload_id", uploadID)
	return nil
}

func (g *s3Group) UploadPart(_ context.Context, t *harness.TestContext) error {
	uploadID := t.GetString("upload_id")
	if uploadID == "" {
		return fmt.Errorf("s3 UploadPart: missing upload_id in state")
	}
	// Multipart parts must be at least 5 MiB on real AWS, but the emulator accepts any size.
	partData := strings.Repeat("x", 1024)
	out, err := awscli.RunOutputWithStdin(t.Endpoint, t.Region, partData,
		"s3api", "upload-part",
		"--bucket", g.bucket(t),
		"--key", "multipart.bin",
		"--part-number", "1",
		"--upload-id", uploadID,
		"--body", "/dev/stdin",
	)
	if err != nil {
		return err
	}
	etag, _ := out["ETag"].(string)
	if etag == "" {
		return fmt.Errorf("s3 UploadPart: missing ETag")
	}
	t.Set("part_etag", strings.Trim(etag, `"`))
	return nil
}

func (g *s3Group) CompleteMultipartUpload(_ context.Context, t *harness.TestContext) error {
	uploadID := t.GetString("upload_id")
	etag := t.GetString("part_etag")
	if uploadID == "" || etag == "" {
		return fmt.Errorf("s3 CompleteMultipartUpload: missing upload_id or part_etag")
	}
	parts := fmt.Sprintf(`{"Parts":[{"PartNumber":1,"ETag":"%s"}]}`, etag)
	completed, err := awscli.RunOutput(t.Endpoint, t.Region,
		"s3api", "complete-multipart-upload",
		"--bucket", g.bucket(t),
		"--key", "multipart.bin",
		"--upload-id", uploadID,
		"--multipart-upload", parts,
	)
	if err != nil {
		return err
	}
	// Verify the object exists
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"s3api", "head-object",
		"--bucket", g.bucket(t),
		"--key", "multipart.bin",
	)
	if err != nil {
		return fmt.Errorf("s3 CompleteMultipartUpload: head-object failed: %w", err)
	}
	if out["ContentLength"] == nil {
		return fmt.Errorf("s3 CompleteMultipartUpload: missing ContentLength")
	}
	// S3 gives a multipart object the MD5 of its parts' binary MD5s, suffixed with the part count (#2232):
	// md5(md5(1 KiB of "x"))-1 for UploadPart's single part.
	const want = `"64a3b22c51ddc101b3e6d623279b60e9-1"`
	if got, _ := completed["ETag"].(string); got != want {
		return fmt.Errorf("s3 CompleteMultipartUpload: ETag = %s, want %s", got, want)
	}
	if got, _ := out["ETag"].(string); got != want {
		return fmt.Errorf("s3 CompleteMultipartUpload: head-object ETag = %s, want %s", got, want)
	}
	return nil
}

func (g *s3Group) AbortMultipartUpload(_ context.Context, t *harness.TestContext) error {
	// Create a fresh upload to abort.
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"s3api", "create-multipart-upload",
		"--bucket", g.bucket(t),
		"--key", "abort.bin",
	)
	if err != nil {
		return err
	}
	uploadID, _ := out["UploadId"].(string)
	if err := awscli.Run(t.Endpoint, t.Region,
		"s3api", "abort-multipart-upload",
		"--bucket", g.bucket(t),
		"--key", "abort.bin",
		"--upload-id", uploadID,
	); err != nil {
		return err
	}
	uploadList, err := awscli.RunOutput(t.Endpoint, t.Region,
		"s3api", "list-multipart-uploads",
		"--bucket", g.bucket(t),
	)
	if err != nil {
		return fmt.Errorf("s3 AbortMultipartUpload: list-multipart-uploads failed: %w", err)
	}
	uploads, _ := uploadList["Uploads"].([]any)
	for _, raw := range uploads {
		if m, ok := raw.(map[string]any); ok && m["UploadId"] == uploadID {
			return fmt.Errorf("s3 AbortMultipartUpload: upload %s still present after abort", uploadID)
		}
	}
	return nil
}

// ─── s3-versioning ───────────────────────────────────────────────────────────

func (g *s3Group) setupVersioning(_ context.Context, t *harness.TestContext) error {
	t.Set("bucket", s3VerNamer.Name(t))
	if err := awscli.Run(t.Endpoint, t.Region, "s3api", "create-bucket", "--bucket", g.bucket(t)); err != nil && !isAlreadyExists(err) {
		return err
	}
	return nil
}

func (g *s3Group) PutBucketVersioning(_ context.Context, t *harness.TestContext) error {
	if err := awscli.Run(t.Endpoint, t.Region,
		"s3api", "put-bucket-versioning",
		"--bucket", g.bucket(t),
		"--versioning-configuration", `{"Status":"Enabled"}`,
	); err != nil {
		return err
	}
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"s3api", "get-bucket-versioning",
		"--bucket", g.bucket(t),
	)
	if err != nil {
		return fmt.Errorf("s3 PutBucketVersioning: get-bucket-versioning failed: %w", err)
	}
	if status, _ := out["Status"].(string); status != "Enabled" {
		return fmt.Errorf("s3 PutBucketVersioning: expected Enabled, got %q", status)
	}
	return nil
}

func (g *s3Group) GetBucketVersioning(_ context.Context, t *harness.TestContext) error {
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"s3api", "get-bucket-versioning",
		"--bucket", g.bucket(t),
	)
	if err != nil {
		return err
	}
	if status, _ := out["Status"].(string); status != "Enabled" {
		return fmt.Errorf("s3 GetBucketVersioning: expected Status=Enabled, got %q", status)
	}
	return nil
}

// ─── s3-tagging ──────────────────────────────────────────────────────────────

func (g *s3Group) setupTagging(_ context.Context, t *harness.TestContext) error {
	t.Set("bucket", s3TagNamer.Name(t))
	if err := awscli.Run(t.Endpoint, t.Region, "s3api", "create-bucket", "--bucket", g.bucket(t)); err != nil && !isAlreadyExists(err) {
		return err
	}
	return awscli.RunWithStdin(t.Endpoint, t.Region, "tag-content",
		"s3api", "put-object",
		"--bucket", g.bucket(t),
		"--key", "obj.txt",
		"--body", "/dev/stdin",
	)
}

func (g *s3Group) PutObjectTagging(_ context.Context, t *harness.TestContext) error {
	if err := awscli.Run(t.Endpoint, t.Region,
		"s3api", "put-object-tagging",
		"--bucket", g.bucket(t),
		"--key", "obj.txt",
		"--tagging", `{"TagSet":[{"Key":"env","Value":"test"}]}`,
	); err != nil {
		return err
	}
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"s3api", "get-object-tagging",
		"--bucket", g.bucket(t),
		"--key", "obj.txt",
	)
	if err != nil {
		return fmt.Errorf("s3 PutObjectTagging: get-object-tagging failed: %w", err)
	}
	tags, _ := out["TagSet"].([]any)
	for _, raw := range tags {
		if m, ok := raw.(map[string]any); ok && m["Key"] == "env" && m["Value"] == "test" {
			return nil
		}
	}
	return fmt.Errorf("s3 PutObjectTagging: env=test tag not found")
}

func (g *s3Group) GetObjectTagging(_ context.Context, t *harness.TestContext) error {
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"s3api", "get-object-tagging",
		"--bucket", g.bucket(t),
		"--key", "obj.txt",
	)
	if err != nil {
		return err
	}
	tags, _ := out["TagSet"].([]any)
	if len(tags) == 0 {
		return fmt.Errorf("s3 GetObjectTagging: expected tags, got none")
	}
	return nil
}

func (g *s3Group) PutBucketTagging(_ context.Context, t *harness.TestContext) error {
	if err := awscli.Run(t.Endpoint, t.Region,
		"s3api", "put-bucket-tagging",
		"--bucket", g.bucket(t),
		"--tagging", `{"TagSet":[{"Key":"project","Value":"overcast"}]}`,
	); err != nil {
		return err
	}
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"s3api", "get-bucket-tagging",
		"--bucket", g.bucket(t),
	)
	if err != nil {
		return fmt.Errorf("s3 PutBucketTagging: get-bucket-tagging failed: %w", err)
	}
	tags, _ := out["TagSet"].([]any)
	for _, raw := range tags {
		if m, ok := raw.(map[string]any); ok && m["Key"] == "project" && m["Value"] == "overcast" {
			return nil
		}
	}
	return fmt.Errorf("s3 PutBucketTagging: project=overcast tag not found")
}

func (g *s3Group) GetBucketTagging(_ context.Context, t *harness.TestContext) error {
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"s3api", "get-bucket-tagging",
		"--bucket", g.bucket(t),
	)
	if err != nil {
		return err
	}
	tags, _ := out["TagSet"].([]any)
	if len(tags) == 0 {
		return fmt.Errorf("s3 GetBucketTagging: expected tags, got none")
	}
	return nil
}

// ─── s3-website ──────────────────────────────────────────────────────────────

func (g *s3Group) setupWebsite(_ context.Context, t *harness.TestContext) error {
	t.Set("bucket", s3WebNamer.Name(t))
	if err := awscli.Run(t.Endpoint, t.Region, "s3api", "create-bucket", "--bucket", g.bucket(t)); err != nil && !isAlreadyExists(err) {
		return err
	}
	return nil
}

func (g *s3Group) PutBucketWebsite(_ context.Context, t *harness.TestContext) error {
	if err := awscli.Run(t.Endpoint, t.Region,
		"s3api", "put-bucket-website",
		"--bucket", g.bucket(t),
		"--website-configuration",
		`{"IndexDocument":{"Suffix":"index.html"},"ErrorDocument":{"Key":"error.html"}}`,
	); err != nil {
		return err
	}
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"s3api", "get-bucket-website",
		"--bucket", g.bucket(t),
	)
	if err != nil {
		return fmt.Errorf("s3 PutBucketWebsite: get-bucket-website failed: %w", err)
	}
	idx, _ := out["IndexDocument"].(map[string]any)
	if idx == nil || idx["Suffix"] != "index.html" {
		return fmt.Errorf("s3 PutBucketWebsite: IndexDocument.Suffix != index.html")
	}
	return nil
}

func (g *s3Group) GetBucketWebsite(_ context.Context, t *harness.TestContext) error {
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"s3api", "get-bucket-website",
		"--bucket", g.bucket(t),
	)
	if err != nil {
		return err
	}
	idx, _ := out["IndexDocument"].(map[string]any)
	if idx == nil || idx["Suffix"] != "index.html" {
		return fmt.Errorf("s3 GetBucketWebsite: expected IndexDocument.Suffix=index.html")
	}
	return nil
}

// ─── s3-cors ─────────────────────────────────────────────────────────────────

func (g *s3Group) setupCors(_ context.Context, t *harness.TestContext) error {
	t.Set("bucket", s3CorsNamer.Name(t))
	if err := awscli.Run(t.Endpoint, t.Region, "s3api", "create-bucket", "--bucket", g.bucket(t)); err != nil && !isAlreadyExists(err) {
		return err
	}
	return nil
}

func (g *s3Group) PutBucketCors(_ context.Context, t *harness.TestContext) error {
	if err := awscli.Run(t.Endpoint, t.Region,
		"s3api", "put-bucket-cors",
		"--bucket", g.bucket(t),
		"--cors-configuration",
		`{"CORSRules":[{"AllowedMethods":["GET","PUT"],"AllowedOrigins":["*"],"AllowedHeaders":["*"]}]}`,
	); err != nil {
		return err
	}
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"s3api", "get-bucket-cors",
		"--bucket", g.bucket(t),
	)
	if err != nil {
		return fmt.Errorf("s3 PutBucketCors: get-bucket-cors failed: %w", err)
	}
	rules, _ := out["CORSRules"].([]any)
	if len(rules) == 0 {
		return fmt.Errorf("s3 PutBucketCors: expected CORS rules, got none")
	}
	return nil
}

func (g *s3Group) GetBucketCors(_ context.Context, t *harness.TestContext) error {
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"s3api", "get-bucket-cors",
		"--bucket", g.bucket(t),
	)
	if err != nil {
		return err
	}
	rules, _ := out["CORSRules"].([]any)
	if len(rules) == 0 {
		return fmt.Errorf("s3 GetBucketCors: expected CORS rules, got none")
	}
	return nil
}

// ─── s3-lifecycle ────────────────────────────────────────────────────────────

// lifecycleConfiguration is the rule this group stores. It is kept as one
// literal so put and get assert against the same intent.
const lifecycleConfiguration = `{"Rules":[{"ID":"expire-logs","Status":"Enabled",` +
	`"Filter":{"Prefix":"logs/"},"Expiration":{"Days":30},` +
	`"Transitions":[{"Days":7,"StorageClass":"GLACIER"}]}]}`

func (g *s3Group) setupLifecycle(_ context.Context, t *harness.TestContext) error {
	t.Set("bucket", s3LcNamer.Name(t))
	if err := awscli.Run(t.Endpoint, t.Region, "s3api", "create-bucket", "--bucket", g.bucket(t)); err != nil && !isAlreadyExists(err) {
		return err
	}
	return nil
}

func (g *s3Group) teardownLifecycle(ctx context.Context, t *harness.TestContext) error {
	// Best-effort: a failure here must not stop the bucket being removed.
	_ = awscli.Run(t.Endpoint, t.Region, "s3api", "delete-bucket-lifecycle", "--bucket", g.bucket(t))
	return g.teardownBucket(ctx, t)
}

// lifecycleRule returns the stored rule with the given ID. Matching on the ID
// rather than taking Rules[0] keeps the assertion about this group's own rule.
func (g *s3Group) lifecycleRule(t *harness.TestContext, id string) (map[string]any, error) {
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"s3api", "get-bucket-lifecycle-configuration",
		"--bucket", g.bucket(t),
	)
	if err != nil {
		return nil, err
	}
	rules, _ := out["Rules"].([]any)
	for _, raw := range rules {
		rule, ok := raw.(map[string]any)
		if ok && rule["ID"] == id {
			return rule, nil
		}
	}
	return nil, fmt.Errorf("lifecycle rule %q not found among %d rules", id, len(rules))
}

func (g *s3Group) PutBucketLifecycleConfiguration(_ context.Context, t *harness.TestContext) error {
	if err := awscli.Run(t.Endpoint, t.Region,
		"s3api", "put-bucket-lifecycle-configuration",
		"--bucket", g.bucket(t),
		"--lifecycle-configuration", lifecycleConfiguration,
	); err != nil {
		return err
	}
	rule, err := g.lifecycleRule(t, "expire-logs")
	if err != nil {
		return fmt.Errorf("s3 PutBucketLifecycleConfiguration: verify failed: %w", err)
	}
	expiration, _ := rule["Expiration"].(map[string]any)
	if days, _ := expiration["Days"].(float64); days != 30 {
		return fmt.Errorf("s3 PutBucketLifecycleConfiguration: expiration = %v, want 30 days", expiration)
	}
	return nil
}

func (g *s3Group) GetBucketLifecycleConfiguration(_ context.Context, t *harness.TestContext) error {
	rule, err := g.lifecycleRule(t, "expire-logs")
	if err != nil {
		return err
	}
	if rule["Status"] != "Enabled" {
		return fmt.Errorf("s3 GetBucketLifecycleConfiguration: status = %v, want Enabled", rule["Status"])
	}
	filter, _ := rule["Filter"].(map[string]any)
	if filter["Prefix"] != "logs/" {
		return fmt.Errorf("s3 GetBucketLifecycleConfiguration: filter = %v, want prefix logs/", filter)
	}
	transitions, _ := rule["Transitions"].([]any)
	if len(transitions) != 1 {
		return fmt.Errorf("s3 GetBucketLifecycleConfiguration: %d transitions, want 1", len(transitions))
	}
	transition, _ := transitions[0].(map[string]any)
	if transition["StorageClass"] != "GLACIER" {
		return fmt.Errorf("s3 GetBucketLifecycleConfiguration: transition = %v, want GLACIER", transition)
	}
	return nil
}

func (g *s3Group) DeleteBucketLifecycle(_ context.Context, t *harness.TestContext) error {
	return awscli.Run(t.Endpoint, t.Region,
		"s3api", "delete-bucket-lifecycle",
		"--bucket", g.bucket(t),
	)
}

func (g *s3Group) GetBucketLifecycleConfigurationAfterDelete(_ context.Context, t *harness.TestContext) error {
	_, err := awscli.RunOutput(t.Endpoint, t.Region,
		"s3api", "get-bucket-lifecycle-configuration",
		"--bucket", g.bucket(t),
	)
	if err == nil {
		return fmt.Errorf("s3 GetBucketLifecycleConfigurationAfterDelete: expected an error, got a configuration")
	}
	if !strings.Contains(err.Error(), "NoSuchLifecycleConfiguration") {
		return fmt.Errorf("s3 GetBucketLifecycleConfigurationAfterDelete: expected NoSuchLifecycleConfiguration, got %w", err)
	}
	return nil
}
