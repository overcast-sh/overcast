package groups

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/overcast-sh/overcast-compat-go-sdk/internal/clients"
	"github.com/overcast-sh/overcast-compat-go-sdk/internal/harness"
)

// S3 returns the S3 service group.
func S3(c *clients.Clients) ServiceGroup {
	s := &s3Group{c: c}
	return ServiceGroup{
		Impls: map[string]harness.TestFn{
			"s3-crud:CreateBucket":                 s.CreateBucket,
			"s3-crud:PutObject":                    s.PutObject,
			"s3-crud:GetObject":                    s.GetObject,
			"s3-crud:HeadObject":                   s.HeadObject,
			"s3-crud:DeleteObject":                 s.DeleteObject,
			"s3-crud:ListObjectsV2":                s.ListObjectsV2,
			"s3-crud:PutObjectMultipleKeys":        s.PutObjectMultipleKeys,
			"s3-crud:ListObjectsV2Delimiter":       s.ListObjectsV2Delimiter,
			"s3-crud:PutObjectFormContentType":     s.PutObjectFormContentType,
			"s3-crud:PutObjectPlusInKey":           s.PutObjectPlusInKey,
			"s3-crud:DeleteObjects":                s.DeleteObjects,
			"s3-crud:DeleteBucket":                 s.DeleteBucket,
			"s3-copy:CopyObject":                   s.CopyObject,
			"s3-copy:CreateSourceBucket":           s.CreateSourceBucket,
			"s3-copy:PutSourceObject":              s.PutSourceObject,
			"s3-multipart:CreateMultipartUpload":   s.CreateMultipartUpload,
			"s3-multipart:UploadPart":              s.UploadPart,
			"s3-multipart:CompleteMultipartUpload": s.CompleteMultipartUpload,
			"s3-multipart:AbortMultipartUpload":    s.AbortMultipartUpload,
			"s3-versioning:PutBucketVersioning":    s.PutBucketVersioning,
			"s3-versioning:GetBucketVersioning":    s.GetBucketVersioning,
			"s3-tagging:PutBucketTagging":          s.PutBucketTagging,
			"s3-tagging:GetBucketTagging":          s.GetBucketTagging,
			"s3-tagging:PutObjectTagging":          s.PutObjectTagging,
			"s3-tagging:GetObjectTagging":          s.GetObjectTagging,
			"s3-website:PutBucketWebsite":          s.PutBucketWebsite,
			"s3-website:GetBucketWebsite":          s.GetBucketWebsite,
			"s3-cors:PutBucketCors":                s.PutBucketCors,
			"s3-cors:GetBucketCors":                s.GetBucketCors,

			"s3-lifecycle:PutBucketLifecycleConfiguration":            s.PutBucketLifecycleConfiguration,
			"s3-lifecycle:GetBucketLifecycleConfiguration":            s.GetBucketLifecycleConfiguration,
			"s3-lifecycle:DeleteBucketLifecycle":                      s.DeleteBucketLifecycle,
			"s3-lifecycle:GetBucketLifecycleConfigurationAfterDelete": s.GetBucketLifecycleConfigurationAfterDelete,
		},
		Setup: map[string]func(context.Context, *harness.TestContext) error{
			"s3-crud":       s.setupCRUD,
			"s3-copy":       s.setupCopy,
			"s3-multipart":  s.setupMultipart,
			"s3-versioning": s.setupVersioning,
			"s3-tagging":    s.setupTagging,
			"s3-website":    s.setupWebsite,
			"s3-cors":       s.setupCors,
			"s3-lifecycle":  s.setupLifecycle,
		},
		Teardown: map[string]func(context.Context, *harness.TestContext) error{
			"s3-crud":       s.teardownBucket("s3_bucket"),
			"s3-copy":       s.teardownCopy,
			"s3-multipart":  s.teardownBucket("s3_mp_bucket"),
			"s3-versioning": s.teardownBucket("s3_ver_bucket"),
			"s3-tagging":    s.teardownBucket("s3_tag_bucket"),
			"s3-website":    s.teardownBucket("s3_web_bucket"),
			"s3-cors":       s.teardownBucket("s3_cors_bucket"),
			"s3-lifecycle":  s.teardownLifecycle,
		},
	}
}

type s3Group struct{ c *clients.Clients }

func (s *s3Group) client() *s3.Client { return s.c.S3() }

func (s *s3Group) createBucket(ctx context.Context, name string) error {
	_, err := s.client().CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(name)})
	return err
}

func (s *s3Group) emptyAndDeleteBucket(ctx context.Context, name string) {
	cl := s.client()
	// Abort any incomplete multipart uploads first.
	mpResp, _ := cl.ListMultipartUploads(ctx, &s3.ListMultipartUploadsInput{Bucket: aws.String(name)})
	if mpResp != nil {
		for _, u := range mpResp.Uploads {
			cl.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{ //nolint:errcheck
				Bucket:   aws.String(name),
				Key:      u.Key,
				UploadId: u.UploadId,
			})
		}
	}
	resp, _ := cl.ListObjectVersions(ctx, &s3.ListObjectVersionsInput{Bucket: aws.String(name)})
	if resp != nil {
		for _, v := range resp.Versions {
			cl.DeleteObject(ctx, &s3.DeleteObjectInput{ //nolint:errcheck
				Bucket:    aws.String(name),
				Key:       v.Key,
				VersionId: v.VersionId,
			})
		}
		for _, dm := range resp.DeleteMarkers {
			cl.DeleteObject(ctx, &s3.DeleteObjectInput{ //nolint:errcheck
				Bucket:    aws.String(name),
				Key:       dm.Key,
				VersionId: dm.VersionId,
			})
		}
	}
	objResp, _ := cl.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(name)})
	if objResp != nil {
		for _, obj := range objResp.Contents {
			cl.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(name), Key: obj.Key}) //nolint:errcheck
		}
	}
	cl.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String(name)}) //nolint:errcheck
}

func (s *s3Group) teardownBucket(key string) func(context.Context, *harness.TestContext) error {
	return func(ctx context.Context, t *harness.TestContext) error {
		name := t.GetString(key)
		if name == "" {
			return nil
		}
		s.emptyAndDeleteBucket(ctx, name)
		return nil
	}
}

// ── s3-crud ───────────────────────────────────────────────────────────────────

func (s *s3Group) setupCRUD(ctx context.Context, t *harness.TestContext) error {
	name := fmt.Sprintf("%s-s3crud", t.RunID)
	if err := s.createBucket(ctx, name); err != nil {
		return err
	}
	t.Set("s3_bucket", name)
	return nil
}

func (s *s3Group) CreateBucket(ctx context.Context, t *harness.TestContext) error {
	name := fmt.Sprintf("%s-s3create", t.RunID)
	if err := s.createBucket(ctx, name); err != nil {
		return err
	}
	defer s.emptyAndDeleteBucket(ctx, name)
	// Verify the bucket appears in ListBuckets
	resp, err := s.client().ListBuckets(ctx, &s3.ListBucketsInput{})
	if err != nil {
		return fmt.Errorf("CreateBucket: ListBuckets failed: %w", err)
	}
	for _, b := range resp.Buckets {
		if aws.ToString(b.Name) == name {
			return nil
		}
	}
	return fmt.Errorf("CreateBucket: bucket %q not found in ListBuckets", name)
}

func (s *s3Group) ListBuckets(ctx context.Context, t *harness.TestContext) error {
	bucket := t.GetString("s3_bucket")
	resp, err := s.client().ListBuckets(ctx, &s3.ListBucketsInput{})
	if err != nil {
		return err
	}
	for _, b := range resp.Buckets {
		if aws.ToString(b.Name) == bucket {
			return nil
		}
	}
	return fmt.Errorf("bucket %q not found in ListBuckets", bucket)
}

func (s *s3Group) PutObject(ctx context.Context, t *harness.TestContext) error {
	bucket := t.GetString("s3_bucket")
	_, err := s.client().PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String("test-key"),
		Body:   strings.NewReader("hello world"),
	})
	if err != nil {
		return err
	}
	resp, err := s.client().HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String("test-key"),
	})
	if err != nil {
		return fmt.Errorf("PutObject: HeadObject verify failed: %w", err)
	}
	if aws.ToInt64(resp.ContentLength) != int64(len("hello world")) {
		return fmt.Errorf("PutObject: expected ContentLength=%d, got %d", len("hello world"), aws.ToInt64(resp.ContentLength))
	}
	return nil
}

func (s *s3Group) GetObject(ctx context.Context, t *harness.TestContext) error {
	bucket := t.GetString("s3_bucket")
	resp, err := s.client().GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String("test-key"),
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if string(body) != "hello world" {
		return fmt.Errorf("GetObject: expected %q, got %q", "hello world", string(body))
	}
	return nil
}

func (s *s3Group) HeadObject(ctx context.Context, t *harness.TestContext) error {
	bucket := t.GetString("s3_bucket")
	resp, err := s.client().HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String("test-key"),
	})
	if err != nil {
		return err
	}
	if aws.ToInt64(resp.ContentLength) != int64(len("hello world")) {
		return fmt.Errorf("HeadObject: expected ContentLength=%d, got %d", len("hello world"), aws.ToInt64(resp.ContentLength))
	}
	return nil
}

// PutObjectFormContentType writes an object whose Content-Type is the one a
// bare HTTP client sends when none is set. Content-Type is metadata on AWS, not
// a parsing instruction, but an emulator that sniffs it to spot AWS Query
// traffic can consume the body before the S3 handler sees it — silently storing
// zero bytes. Every SDK picks a sensible default type, so only application code
// that sets this one explicitly reaches the case.
func (s *s3Group) PutObjectFormContentType(ctx context.Context, t *harness.TestContext) error {
	bucket := t.GetString("s3_bucket")
	const key = "form-content-type"
	const body = "hello-body-check"

	if _, err := s.client().PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(bucket),
		Key:         aws.String(key),
		Body:        strings.NewReader(body),
		ContentType: aws.String("application/x-www-form-urlencoded"),
	}); err != nil {
		return err
	}

	resp, err := s.client().GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("PutObjectFormContentType: GetObject verify failed: %w", err)
	}
	defer resp.Body.Close()
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if string(got) != body {
		return fmt.Errorf("PutObjectFormContentType: expected body %q, got %q", body, string(got))
	}
	if ct := aws.ToString(resp.ContentType); ct != "application/x-www-form-urlencoded" {
		return fmt.Errorf("PutObjectFormContentType: expected ContentType %q, got %q",
			"application/x-www-form-urlencoded", ct)
	}
	// Leave the group bucket as it was found: some suites' DeleteBucket test
	// deletes this shared bucket and needs it empty by then.
	return s.deleteCrudKey(ctx, bucket, key)
}

// deleteCrudKey removes a key a test added to the shared s3-crud bucket, so
// the group's later DeleteBucket still sees an empty bucket.
func (s *s3Group) deleteCrudKey(ctx context.Context, bucket, key string) error {
	if _, err := s.client().DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	}); err != nil {
		return fmt.Errorf("cleanup: DeleteObject %q: %w", key, err)
	}
	return nil
}

// PutObjectPlusInKey writes an object whose key contains "+". SDKs percent-encode
// it as %2B in the request path — unlike a space or a multi-byte character, whose
// encodings survive a round trip through Go's URL canonicalisation — so a server
// that reads the raw path without decoding stores the literal three characters
// "%2B" as part of the key.
func (s *s3Group) PutObjectPlusInKey(ctx context.Context, t *harness.TestContext) error {
	bucket := t.GetString("s3_bucket")
	const key = "plusonly/a+b.txt"
	const body = "plus-body"

	if _, err := s.client().PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
		Body:   strings.NewReader(body),
	}); err != nil {
		return err
	}

	resp, err := s.client().GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("PutObjectPlusInKey: GetObject verify failed: %w", err)
	}
	defer resp.Body.Close()
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if string(got) != body {
		return fmt.Errorf("PutObjectPlusInKey: expected body %q, got %q", body, string(got))
	}

	listed, err := s.client().ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket: aws.String(bucket),
		Prefix: aws.String("plusonly/"),
	})
	if err != nil {
		return fmt.Errorf("PutObjectPlusInKey: ListObjectsV2 verify failed: %w", err)
	}
	keys := make([]string, 0, len(listed.Contents))
	found := false
	for _, obj := range listed.Contents {
		k := aws.ToString(obj.Key)
		keys = append(keys, k)
		if k == key {
			found = true
		}
	}
	if !found {
		return fmt.Errorf("PutObjectPlusInKey: expected key %q in ListObjectsV2, got %v", key, keys)
	}
	return s.deleteCrudKey(ctx, bucket, key)
}

func (s *s3Group) DeleteObject(ctx context.Context, t *harness.TestContext) error {
	bucket := t.GetString("s3_bucket")
	_, err := s.client().DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String("test-key"),
	})
	if err != nil {
		return err
	}
	// Verify the key is gone
	resp, err := s.client().ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(bucket)})
	if err != nil {
		return fmt.Errorf("DeleteObject: ListObjectsV2 failed: %w", err)
	}
	for _, obj := range resp.Contents {
		if aws.ToString(obj.Key) == "test-key" {
			return fmt.Errorf("DeleteObject: key 'test-key' still present after delete")
		}
	}
	return nil
}

func (s *s3Group) ListObjects(ctx context.Context, t *harness.TestContext) error {
	bucket := t.GetString("s3_bucket")
	// Put a fresh object
	s.client().PutObject(ctx, &s3.PutObjectInput{ //nolint:errcheck
		Bucket: aws.String(bucket),
		Key:    aws.String("list-key"),
		Body:   strings.NewReader("data"),
	})
	resp, err := s.client().ListObjects(ctx, &s3.ListObjectsInput{Bucket: aws.String(bucket)})
	if err != nil {
		return err
	}
	if len(resp.Contents) == 0 {
		return fmt.Errorf("ListObjects: expected ≥1 object")
	}
	return nil
}

func (s *s3Group) ListObjectsV2(ctx context.Context, t *harness.TestContext) error {
	bucket := t.GetString("s3_bucket")
	resp, err := s.client().ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(bucket)})
	if err != nil {
		return err
	}
	if len(resp.Contents) == 0 {
		return fmt.Errorf("ListObjectsV2: expected ≥1 object")
	}
	return nil
}

func (s *s3Group) PutObjectMultipleKeys(ctx context.Context, t *harness.TestContext) error {
	bucket := t.GetString("s3_bucket")
	for _, key := range []string{"multi-a", "multi-b", "multi-c"} {
		_, err := s.client().PutObject(ctx, &s3.PutObjectInput{
			Bucket: aws.String(bucket),
			Key:    aws.String(key),
			Body:   strings.NewReader("data-" + key),
		})
		if err != nil {
			return fmt.Errorf("PutObject %q: %w", key, err)
		}
	}
	resp, err := s.client().ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket: aws.String(bucket),
		Prefix: aws.String("multi-"),
	})
	if err != nil {
		return fmt.Errorf("PutObjectMultipleKeys: ListObjectsV2 verify failed: %w", err)
	}
	if len(resp.Contents) < 3 {
		return fmt.Errorf("PutObjectMultipleKeys: expected ≥3 objects with prefix multi-, got %d", len(resp.Contents))
	}
	return nil
}

func (s *s3Group) ListObjectsV2Delimiter(ctx context.Context, t *harness.TestContext) error {
	bucket := t.GetString("s3_bucket")
	// Put objects under a "folder" prefix
	for _, key := range []string{"folder/obj1", "folder/obj2"} {
		s.client().PutObject(ctx, &s3.PutObjectInput{ //nolint:errcheck
			Bucket: aws.String(bucket),
			Key:    aws.String(key),
			Body:   strings.NewReader("data"),
		})
	}
	resp, err := s.client().ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket:    aws.String(bucket),
		Delimiter: aws.String("/"),
	})
	if err != nil {
		return err
	}
	for _, cp := range resp.CommonPrefixes {
		if aws.ToString(cp.Prefix) == "folder/" {
			return nil
		}
	}
	return fmt.Errorf("ListObjectsV2Delimiter: \"folder/\" not found in CommonPrefixes")
}

func (s *s3Group) DeleteObjects(ctx context.Context, t *harness.TestContext) error {
	bucket := t.GetString("s3_bucket")
	// Put 2 objects to delete
	for _, key := range []string{"del-multi-a", "del-multi-b"} {
		s.client().PutObject(ctx, &s3.PutObjectInput{ //nolint:errcheck
			Bucket: aws.String(bucket),
			Key:    aws.String(key),
			Body:   strings.NewReader("data"),
		})
	}
	resp, err := s.client().DeleteObjects(ctx, &s3.DeleteObjectsInput{
		Bucket: aws.String(bucket),
		Delete: &s3types.Delete{
			Objects: []s3types.ObjectIdentifier{
				{Key: aws.String("del-multi-a")},
				{Key: aws.String("del-multi-b")},
			},
		},
	})
	if err != nil {
		return err
	}
	if len(resp.Deleted) != 2 {
		return fmt.Errorf("DeleteObjects: expected 2 deleted, got %d", len(resp.Deleted))
	}
	return nil
}

func (s *s3Group) DeleteBucket(ctx context.Context, t *harness.TestContext) error {
	name := fmt.Sprintf("%s-s3del", t.RunID)
	if err := s.createBucket(ctx, name); err != nil {
		return err
	}
	_, err := s.client().DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String(name)})
	if err != nil {
		return err
	}
	resp, err := s.client().ListBuckets(ctx, &s3.ListBucketsInput{})
	if err != nil {
		return fmt.Errorf("DeleteBucket: ListBuckets verify failed: %w", err)
	}
	for _, b := range resp.Buckets {
		if aws.ToString(b.Name) == name {
			return fmt.Errorf("DeleteBucket: bucket %q still present after delete", name)
		}
	}
	return nil
}

// ── s3-copy ───────────────────────────────────────────────────────────────────

func (s *s3Group) setupCopy(ctx context.Context, t *harness.TestContext) error {
	name := fmt.Sprintf("%s-s3copy", t.RunID)
	if err := s.createBucket(ctx, name); err != nil {
		return err
	}
	if _, err := s.client().PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(name),
		Key:         aws.String("src"),
		Body:        strings.NewReader("copy-content"),
		ContentType: aws.String("text/plain"),
	}); err != nil {
		return err
	}
	t.Set("s3_copy_bucket", name)
	return nil
}

func (s *s3Group) teardownCopy(ctx context.Context, t *harness.TestContext) error {
	for _, key := range []string{"s3_copy_bucket", "s3_copy_src_bucket"} {
		name := t.GetString(key)
		if name != "" {
			s.emptyAndDeleteBucket(ctx, name)
		}
	}
	return nil
}

func (s *s3Group) CreateSourceBucket(ctx context.Context, t *harness.TestContext) error {
	name := fmt.Sprintf("%s-s3copysrc", t.RunID)
	if err := s.createBucket(ctx, name); err != nil {
		return err
	}
	t.Set("s3_copy_src_bucket", name)
	return nil
}

func (s *s3Group) PutSourceObject(ctx context.Context, t *harness.TestContext) error {
	bucket := t.GetString("s3_copy_src_bucket")
	_, err := s.client().PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String("src-key"),
		Body:   strings.NewReader("source data"),
	})
	return err
}

func (s *s3Group) CopyObject(ctx context.Context, t *harness.TestContext) error {
	bucket := t.GetString("s3_copy_bucket")
	_, err := s.client().CopyObject(ctx, &s3.CopyObjectInput{
		Bucket:     aws.String(bucket),
		Key:        aws.String("dst"),
		CopySource: aws.String(bucket + "/src"),
	})
	if err != nil {
		return err
	}
	resp, err := s.client().GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String("dst"),
	})
	if err != nil {
		return fmt.Errorf("CopyObject: GetObject verify failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if string(body) != "copy-content" {
		return fmt.Errorf("CopyObject: expected %q, got %q", "copy-content", string(body))
	}
	return nil
}

func (s *s3Group) GetObjectMetadata(ctx context.Context, t *harness.TestContext) error {
	bucket := t.GetString("s3_copy_bucket")
	resp, err := s.client().HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String("src"),
	})
	if err != nil {
		return err
	}
	if aws.ToString(resp.ContentType) == "" {
		return fmt.Errorf("GetObjectMetadata: ContentType is empty")
	}
	return nil
}

func (s *s3Group) CopyObjectWithMetadata(ctx context.Context, t *harness.TestContext) error {
	bucket := t.GetString("s3_copy_bucket")
	_, err := s.client().CopyObject(ctx, &s3.CopyObjectInput{
		Bucket:            aws.String(bucket),
		Key:               aws.String("dst-meta"),
		CopySource:        aws.String(bucket + "/src"),
		MetadataDirective: s3types.MetadataDirectiveReplace,
		Metadata:          map[string]string{"x-custom": "compat"},
	})
	if err != nil {
		return err
	}
	resp, err := s.client().HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String("dst-meta"),
	})
	if err != nil {
		return fmt.Errorf("CopyObjectWithMetadata: HeadObject verify failed: %w", err)
	}
	if resp.Metadata["x-custom"] != "compat" {
		return fmt.Errorf("CopyObjectWithMetadata: expected metadata x-custom=compat, got %q", resp.Metadata["x-custom"])
	}
	return nil
}

// ── s3-multipart ──────────────────────────────────────────────────────────────

func (s *s3Group) setupMultipart(ctx context.Context, t *harness.TestContext) error {
	name := fmt.Sprintf("%s-s3mp", t.RunID)
	if err := s.createBucket(ctx, name); err != nil {
		return err
	}
	t.Set("s3_mp_bucket", name)
	return nil
}

func (s *s3Group) CreateMultipartUpload(ctx context.Context, t *harness.TestContext) error {
	bucket := t.GetString("s3_mp_bucket")
	resp, err := s.client().CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
		Bucket: aws.String(bucket),
		Key:    aws.String("mp-key"),
	})
	if err != nil {
		return err
	}
	t.Set("s3_upload_id", aws.ToString(resp.UploadId))
	return nil
}

func (s *s3Group) UploadPart(ctx context.Context, t *harness.TestContext) error {
	bucket := t.GetString("s3_mp_bucket")
	uploadID := t.GetString("s3_upload_id")
	part := bytes.Repeat([]byte("x"), 5*1024*1024) // 5 MiB minimum for AWS
	resp, err := s.client().UploadPart(ctx, &s3.UploadPartInput{
		Bucket:     aws.String(bucket),
		Key:        aws.String("mp-key"),
		UploadId:   aws.String(uploadID),
		PartNumber: aws.Int32(1),
		Body:       bytes.NewReader(part),
	})
	if err != nil {
		return err
	}
	t.Set("s3_part_etag", aws.ToString(resp.ETag))
	return nil
}

func (s *s3Group) CompleteMultipartUpload(ctx context.Context, t *harness.TestContext) error {
	bucket := t.GetString("s3_mp_bucket")
	uploadID := t.GetString("s3_upload_id")
	etag := t.GetString("s3_part_etag")
	out, err := s.client().CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket:   aws.String(bucket),
		Key:      aws.String("mp-key"),
		UploadId: aws.String(uploadID),
		MultipartUpload: &s3types.CompletedMultipartUpload{
			Parts: []s3types.CompletedPart{
				{PartNumber: aws.Int32(1), ETag: aws.String(etag)},
			},
		},
	})
	if err != nil {
		return err
	}
	head, err := s.client().HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String("mp-key"),
	})
	if err != nil {
		return fmt.Errorf("CompleteMultipartUpload: HeadObject verify failed: %w", err)
	}
	// S3 gives a multipart object the MD5 of its parts' binary MD5s, suffixed with the part count (#2232):
	// md5(md5(5 MiB of "x"))-1 for UploadPart's single part.
	const want = `"9dc2b5968307ce171ec8a2a9251a60d9-1"`
	if got := aws.ToString(out.ETag); got != want {
		return fmt.Errorf("CompleteMultipartUpload: ETag = %s, want %s", got, want)
	}
	if got := aws.ToString(head.ETag); got != want {
		return fmt.Errorf("CompleteMultipartUpload: HeadObject ETag = %s, want %s", got, want)
	}
	return nil
}

func (s *s3Group) AbortMultipartUpload(ctx context.Context, t *harness.TestContext) error {
	bucket := t.GetString("s3_mp_bucket")
	// Start a fresh upload to abort
	resp, err := s.client().CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
		Bucket: aws.String(bucket),
		Key:    aws.String("mp-abort"),
	})
	if err != nil {
		return err
	}
	_, err = s.client().AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
		Bucket:   aws.String(bucket),
		Key:      aws.String("mp-abort"),
		UploadId: resp.UploadId,
	})
	return err
}

func (s *s3Group) ListMultipartUploads(ctx context.Context, t *harness.TestContext) error {
	bucket := t.GetString("s3_mp_bucket")
	_, err := s.client().ListMultipartUploads(ctx, &s3.ListMultipartUploadsInput{
		Bucket: aws.String(bucket),
	})
	return err
}

// ── s3-versioning ─────────────────────────────────────────────────────────────

func (s *s3Group) setupVersioning(ctx context.Context, t *harness.TestContext) error {
	name := fmt.Sprintf("%s-s3ver", t.RunID)
	if err := s.createBucket(ctx, name); err != nil {
		return err
	}
	t.Set("s3_ver_bucket", name)
	return nil
}

func (s *s3Group) PutBucketVersioning(ctx context.Context, t *harness.TestContext) error {
	bucket := t.GetString("s3_ver_bucket")
	_, err := s.client().PutBucketVersioning(ctx, &s3.PutBucketVersioningInput{
		Bucket: aws.String(bucket),
		VersioningConfiguration: &s3types.VersioningConfiguration{
			Status: s3types.BucketVersioningStatusEnabled,
		},
	})
	if err != nil {
		return err
	}
	resp, err := s.client().GetBucketVersioning(ctx, &s3.GetBucketVersioningInput{
		Bucket: aws.String(bucket),
	})
	if err != nil {
		return fmt.Errorf("PutBucketVersioning: GetBucketVersioning verify failed: %w", err)
	}
	if resp.Status != s3types.BucketVersioningStatusEnabled {
		return fmt.Errorf("PutBucketVersioning: expected Enabled, got %q", resp.Status)
	}
	return nil
}

func (s *s3Group) GetBucketVersioning(ctx context.Context, t *harness.TestContext) error {
	bucket := t.GetString("s3_ver_bucket")
	resp, err := s.client().GetBucketVersioning(ctx, &s3.GetBucketVersioningInput{
		Bucket: aws.String(bucket),
	})
	if err != nil {
		return err
	}
	if resp.Status != s3types.BucketVersioningStatusEnabled {
		return fmt.Errorf("GetBucketVersioning: expected Enabled, got %q", resp.Status)
	}
	return nil
}

func (s *s3Group) PutObjectVersioned(ctx context.Context, t *harness.TestContext) error {
	bucket := t.GetString("s3_ver_bucket")
	resp, err := s.client().PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String("versioned-key"),
		Body:   strings.NewReader("v1"),
	})
	if err != nil {
		return err
	}
	t.Set("s3_version_id", aws.ToString(resp.VersionId))
	return nil
}

func (s *s3Group) GetObjectVersion(ctx context.Context, t *harness.TestContext) error {
	bucket := t.GetString("s3_ver_bucket")
	vid := t.GetString("s3_version_id")
	resp, err := s.client().GetObject(ctx, &s3.GetObjectInput{
		Bucket:    aws.String(bucket),
		Key:       aws.String("versioned-key"),
		VersionId: aws.String(vid),
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

func (s *s3Group) ListObjectVersions(ctx context.Context, t *harness.TestContext) error {
	bucket := t.GetString("s3_ver_bucket")
	resp, err := s.client().ListObjectVersions(ctx, &s3.ListObjectVersionsInput{
		Bucket: aws.String(bucket),
	})
	if err != nil {
		return err
	}
	if len(resp.Versions) == 0 {
		return fmt.Errorf("ListObjectVersions: no versions returned")
	}
	return nil
}

func (s *s3Group) DeleteObjectVersion(ctx context.Context, t *harness.TestContext) error {
	bucket := t.GetString("s3_ver_bucket")
	vid := t.GetString("s3_version_id")
	_, err := s.client().DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket:    aws.String(bucket),
		Key:       aws.String("versioned-key"),
		VersionId: aws.String(vid),
	})
	if err != nil {
		return err
	}
	resp, err := s.client().ListObjectVersions(ctx, &s3.ListObjectVersionsInput{
		Bucket: aws.String(bucket),
		Prefix: aws.String("versioned-key"),
	})
	if err != nil {
		return fmt.Errorf("DeleteObjectVersion: ListObjectVersions verify failed: %w", err)
	}
	for _, v := range resp.Versions {
		if aws.ToString(v.VersionId) == vid {
			return fmt.Errorf("DeleteObjectVersion: version %q still present", vid)
		}
	}
	return nil
}

// ── s3-tagging ────────────────────────────────────────────────────────────────

func (s *s3Group) setupTagging(ctx context.Context, t *harness.TestContext) error {
	name := fmt.Sprintf("%s-s3tag", t.RunID)
	if err := s.createBucket(ctx, name); err != nil {
		return err
	}
	s.client().PutObject(ctx, &s3.PutObjectInput{ //nolint:errcheck
		Bucket: aws.String(name),
		Key:    aws.String("tag-key"),
		Body:   strings.NewReader("tagged"),
	})
	t.Set("s3_tag_bucket", name)
	return nil
}

func (s *s3Group) PutBucketTagging(ctx context.Context, t *harness.TestContext) error {
	bucket := t.GetString("s3_tag_bucket")
	_, err := s.client().PutBucketTagging(ctx, &s3.PutBucketTaggingInput{
		Bucket: aws.String(bucket),
		Tagging: &s3types.Tagging{
			TagSet: []s3types.Tag{{Key: aws.String("env"), Value: aws.String("test")}},
		},
	})
	if err != nil {
		return err
	}
	resp, err := s.client().GetBucketTagging(ctx, &s3.GetBucketTaggingInput{
		Bucket: aws.String(bucket),
	})
	if err != nil {
		return fmt.Errorf("PutBucketTagging: GetBucketTagging verify failed: %w", err)
	}
	for _, tag := range resp.TagSet {
		if aws.ToString(tag.Key) == "env" && aws.ToString(tag.Value) == "test" {
			return nil
		}
	}
	return fmt.Errorf("PutBucketTagging: env=test tag not found")
}

func (s *s3Group) GetBucketTagging(ctx context.Context, t *harness.TestContext) error {
	bucket := t.GetString("s3_tag_bucket")
	resp, err := s.client().GetBucketTagging(ctx, &s3.GetBucketTaggingInput{
		Bucket: aws.String(bucket),
	})
	if err != nil {
		return err
	}
	for _, tag := range resp.TagSet {
		if aws.ToString(tag.Key) == "env" && aws.ToString(tag.Value) == "test" {
			return nil
		}
	}
	return fmt.Errorf("GetBucketTagging: env=test tag not found")
}

func (s *s3Group) PutObjectTagging(ctx context.Context, t *harness.TestContext) error {
	bucket := t.GetString("s3_tag_bucket")
	_, err := s.client().PutObjectTagging(ctx, &s3.PutObjectTaggingInput{
		Bucket: aws.String(bucket),
		Key:    aws.String("tag-key"),
		Tagging: &s3types.Tagging{
			TagSet: []s3types.Tag{{Key: aws.String("env"), Value: aws.String("compat")}},
		},
	})
	if err != nil {
		return err
	}
	resp, err := s.client().GetObjectTagging(ctx, &s3.GetObjectTaggingInput{
		Bucket: aws.String(bucket),
		Key:    aws.String("tag-key"),
	})
	if err != nil {
		return fmt.Errorf("PutObjectTagging: GetObjectTagging verify failed: %w", err)
	}
	for _, tag := range resp.TagSet {
		if aws.ToString(tag.Key) == "env" && aws.ToString(tag.Value) == "compat" {
			return nil
		}
	}
	return fmt.Errorf("PutObjectTagging: env=compat tag not found")
}

func (s *s3Group) GetObjectTagging(ctx context.Context, t *harness.TestContext) error {
	bucket := t.GetString("s3_tag_bucket")
	resp, err := s.client().GetObjectTagging(ctx, &s3.GetObjectTaggingInput{
		Bucket: aws.String(bucket),
		Key:    aws.String("tag-key"),
	})
	if err != nil {
		return err
	}
	for _, tag := range resp.TagSet {
		if aws.ToString(tag.Key) == "env" && aws.ToString(tag.Value) == "compat" {
			return nil
		}
	}
	return fmt.Errorf("GetObjectTagging: env=compat tag not found")
}

func (s *s3Group) DeleteObjectTagging(ctx context.Context, t *harness.TestContext) error {
	bucket := t.GetString("s3_tag_bucket")
	_, err := s.client().DeleteObjectTagging(ctx, &s3.DeleteObjectTaggingInput{
		Bucket: aws.String(bucket),
		Key:    aws.String("tag-key"),
	})
	if err != nil {
		return err
	}
	resp, err := s.client().GetObjectTagging(ctx, &s3.GetObjectTaggingInput{
		Bucket: aws.String(bucket),
		Key:    aws.String("tag-key"),
	})
	if err != nil {
		return fmt.Errorf("DeleteObjectTagging: GetObjectTagging verify failed: %w", err)
	}
	if len(resp.TagSet) > 0 {
		return fmt.Errorf("DeleteObjectTagging: expected 0 tags, got %d", len(resp.TagSet))
	}
	return nil
}

// ── s3-website ────────────────────────────────────────────────────────────────

func (s *s3Group) setupWebsite(ctx context.Context, t *harness.TestContext) error {
	name := fmt.Sprintf("%s-s3web", t.RunID)
	if err := s.createBucket(ctx, name); err != nil {
		return err
	}
	t.Set("s3_web_bucket", name)
	return nil
}

func (s *s3Group) PutBucketWebsite(ctx context.Context, t *harness.TestContext) error {
	bucket := t.GetString("s3_web_bucket")
	_, err := s.client().PutBucketWebsite(ctx, &s3.PutBucketWebsiteInput{
		Bucket: aws.String(bucket),
		WebsiteConfiguration: &s3types.WebsiteConfiguration{
			IndexDocument: &s3types.IndexDocument{Suffix: aws.String("index.html")},
			ErrorDocument: &s3types.ErrorDocument{Key: aws.String("error.html")},
		},
	})
	if err != nil {
		return err
	}
	resp, err := s.client().GetBucketWebsite(ctx, &s3.GetBucketWebsiteInput{Bucket: aws.String(bucket)})
	if err != nil {
		return fmt.Errorf("PutBucketWebsite: GetBucketWebsite verify failed: %w", err)
	}
	if resp.IndexDocument == nil || aws.ToString(resp.IndexDocument.Suffix) != "index.html" {
		return fmt.Errorf("PutBucketWebsite: expected IndexDocument.Suffix=index.html")
	}
	return nil
}

func (s *s3Group) GetBucketWebsite(ctx context.Context, t *harness.TestContext) error {
	bucket := t.GetString("s3_web_bucket")
	resp, err := s.client().GetBucketWebsite(ctx, &s3.GetBucketWebsiteInput{Bucket: aws.String(bucket)})
	if err != nil {
		return err
	}
	if resp.IndexDocument == nil || aws.ToString(resp.IndexDocument.Suffix) == "" {
		return fmt.Errorf("GetBucketWebsite: missing IndexDocument")
	}
	return nil
}

func (s *s3Group) DeleteBucketWebsite(ctx context.Context, t *harness.TestContext) error {
	bucket := t.GetString("s3_web_bucket")
	_, err := s.client().DeleteBucketWebsite(ctx, &s3.DeleteBucketWebsiteInput{Bucket: aws.String(bucket)})
	if err != nil {
		return err
	}
	// Verify website config is removed (GetBucketWebsite should fail)
	_, err = s.client().GetBucketWebsite(ctx, &s3.GetBucketWebsiteInput{Bucket: aws.String(bucket)})
	if err == nil {
		return fmt.Errorf("DeleteBucketWebsite: GetBucketWebsite should fail after deletion")
	}
	return nil
}

// ── s3-cors ───────────────────────────────────────────────────────────────────

func (s *s3Group) setupCors(ctx context.Context, t *harness.TestContext) error {
	name := fmt.Sprintf("%s-s3cors", t.RunID)
	if err := s.createBucket(ctx, name); err != nil {
		return err
	}
	t.Set("s3_cors_bucket", name)
	return nil
}

func (s *s3Group) PutBucketCors(ctx context.Context, t *harness.TestContext) error {
	bucket := t.GetString("s3_cors_bucket")
	_, err := s.client().PutBucketCors(ctx, &s3.PutBucketCorsInput{
		Bucket: aws.String(bucket),
		CORSConfiguration: &s3types.CORSConfiguration{
			CORSRules: []s3types.CORSRule{{
				AllowedMethods: []string{"GET", "POST"},
				AllowedOrigins: []string{"*"},
				AllowedHeaders: []string{"*"},
			}},
		},
	})
	if err != nil {
		return err
	}
	resp, err := s.client().GetBucketCors(ctx, &s3.GetBucketCorsInput{Bucket: aws.String(bucket)})
	if err != nil {
		return fmt.Errorf("PutBucketCors: GetBucketCors verify failed: %w", err)
	}
	if len(resp.CORSRules) == 0 {
		return fmt.Errorf("PutBucketCors: expected ≥1 CORS rule")
	}
	return nil
}

func (s *s3Group) GetBucketCors(ctx context.Context, t *harness.TestContext) error {
	bucket := t.GetString("s3_cors_bucket")
	resp, err := s.client().GetBucketCors(ctx, &s3.GetBucketCorsInput{Bucket: aws.String(bucket)})
	if err != nil {
		return err
	}
	if len(resp.CORSRules) == 0 {
		return fmt.Errorf("GetBucketCors: no CORS rules returned")
	}
	return nil
}

func (s *s3Group) DeleteBucketCors(ctx context.Context, t *harness.TestContext) error {
	bucket := t.GetString("s3_cors_bucket")
	_, err := s.client().DeleteBucketCors(ctx, &s3.DeleteBucketCorsInput{Bucket: aws.String(bucket)})
	if err != nil {
		return err
	}
	// Verify CORS config is removed
	_, err = s.client().GetBucketCors(ctx, &s3.GetBucketCorsInput{Bucket: aws.String(bucket)})
	if err == nil {
		return fmt.Errorf("DeleteBucketCors: GetBucketCors should fail after deletion")
	}
	return nil
}

// ── s3-lifecycle ──────────────────────────────────────────────────────────────

func (s *s3Group) setupLifecycle(ctx context.Context, t *harness.TestContext) error {
	name := fmt.Sprintf("%s-s3lifecycle", t.RunID)
	if err := s.createBucket(ctx, name); err != nil {
		return err
	}
	t.Set("s3_lifecycle_bucket", name)
	return nil
}

func (s *s3Group) teardownLifecycle(ctx context.Context, t *harness.TestContext) error {
	name := t.GetString("s3_lifecycle_bucket")
	if name == "" {
		return nil
	}
	//nolint:errcheck // teardown is best-effort: one failure must not block the rest
	s.client().DeleteBucketLifecycle(ctx, &s3.DeleteBucketLifecycleInput{Bucket: aws.String(name)})
	s.emptyAndDeleteBucket(ctx, name)
	return nil
}

// lifecycleRule returns the stored rule with the given ID, matching on the ID
// rather than taking the first rule back — a group must assert on its own
// resource, not on whatever arrived first.
func (s *s3Group) lifecycleRule(ctx context.Context, bucket, id string) (*s3types.LifecycleRule, error) {
	resp, err := s.client().GetBucketLifecycleConfiguration(ctx, &s3.GetBucketLifecycleConfigurationInput{
		Bucket: aws.String(bucket),
	})
	if err != nil {
		return nil, err
	}
	for i := range resp.Rules {
		if aws.ToString(resp.Rules[i].ID) == id {
			return &resp.Rules[i], nil
		}
	}
	return nil, fmt.Errorf("lifecycle rule %q not found among %d rules", id, len(resp.Rules))
}

func (s *s3Group) PutBucketLifecycleConfiguration(ctx context.Context, t *harness.TestContext) error {
	bucket := t.GetString("s3_lifecycle_bucket")
	_, err := s.client().PutBucketLifecycleConfiguration(ctx, &s3.PutBucketLifecycleConfigurationInput{
		Bucket: aws.String(bucket),
		LifecycleConfiguration: &s3types.BucketLifecycleConfiguration{
			Rules: []s3types.LifecycleRule{{
				ID:         aws.String("expire-logs"),
				Status:     s3types.ExpirationStatusEnabled,
				Filter:     &s3types.LifecycleRuleFilter{Prefix: aws.String("logs/")},
				Expiration: &s3types.LifecycleExpiration{Days: aws.Int32(30)},
				Transitions: []s3types.Transition{{
					Days:         aws.Int32(7),
					StorageClass: s3types.TransitionStorageClassGlacier,
				}},
			}},
		},
	})
	if err != nil {
		return err
	}
	rule, err := s.lifecycleRule(ctx, bucket, "expire-logs")
	if err != nil {
		return fmt.Errorf("PutBucketLifecycleConfiguration: verify failed: %w", err)
	}
	if rule.Expiration == nil || aws.ToInt32(rule.Expiration.Days) != 30 {
		return fmt.Errorf("PutBucketLifecycleConfiguration: expiration = %v, want 30 days", rule.Expiration)
	}
	return nil
}

func (s *s3Group) GetBucketLifecycleConfiguration(ctx context.Context, t *harness.TestContext) error {
	bucket := t.GetString("s3_lifecycle_bucket")
	rule, err := s.lifecycleRule(ctx, bucket, "expire-logs")
	if err != nil {
		return err
	}
	if rule.Status != s3types.ExpirationStatusEnabled {
		return fmt.Errorf("GetBucketLifecycleConfiguration: status = %q, want Enabled", rule.Status)
	}
	if rule.Filter == nil || aws.ToString(rule.Filter.Prefix) != "logs/" {
		return fmt.Errorf("GetBucketLifecycleConfiguration: filter = %v, want prefix logs/", rule.Filter)
	}
	if len(rule.Transitions) != 1 || rule.Transitions[0].StorageClass != s3types.TransitionStorageClassGlacier {
		return fmt.Errorf("GetBucketLifecycleConfiguration: transitions = %v, want one GLACIER transition", rule.Transitions)
	}
	return nil
}

func (s *s3Group) DeleteBucketLifecycle(ctx context.Context, t *harness.TestContext) error {
	bucket := t.GetString("s3_lifecycle_bucket")
	_, err := s.client().DeleteBucketLifecycle(ctx, &s3.DeleteBucketLifecycleInput{Bucket: aws.String(bucket)})
	return err
}

func (s *s3Group) GetBucketLifecycleConfigurationAfterDelete(ctx context.Context, t *harness.TestContext) error {
	bucket := t.GetString("s3_lifecycle_bucket")
	resp, err := s.client().GetBucketLifecycleConfiguration(ctx, &s3.GetBucketLifecycleConfigurationInput{
		Bucket: aws.String(bucket),
	})
	if err == nil {
		return fmt.Errorf("GetBucketLifecycleConfigurationAfterDelete: expected an error, got %d rules", len(resp.Rules))
	}
	if !strings.Contains(err.Error(), "NoSuchLifecycleConfiguration") {
		return fmt.Errorf("GetBucketLifecycleConfigurationAfterDelete: expected NoSuchLifecycleConfiguration, got %w", err)
	}
	return nil
}
