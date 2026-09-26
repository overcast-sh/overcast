package io.overcast.compat.groups;

import io.overcast.compat.clients.AwsClients;
import io.overcast.compat.harness.Assertions;
import io.overcast.compat.harness.TestContext;
import io.overcast.compat.harness.TestFn;
import software.amazon.awssdk.core.sync.RequestBody;
import software.amazon.awssdk.core.sync.ResponseTransformer;
import software.amazon.awssdk.services.s3.S3Client;
import software.amazon.awssdk.services.s3.model.*;

import java.nio.charset.StandardCharsets;
import java.util.List;
import java.util.Map;

/**
 * S3 compatibility test group.
 *
 * <p>Groups: s3-crud, s3-copy, s3-multipart, s3-versioning, s3-tagging,
 * s3-website, s3-cors, s3-lifecycle.
 */
public final class S3Group implements ServiceGroup {

    private final AwsClients clients;

    public S3Group(AwsClients clients) {
        this.clients = clients;
    }

    private S3Client s3() { return clients.s3(); }

    @Override
    public Map<String, TestFn> impls() {
        return Map.ofEntries(
                Map.entry("s3-crud:CreateBucket",                                    this::createBucket),
                Map.entry("s3-crud:PutObject",                                       this::putObject),
                Map.entry("s3-crud:HeadObject",                                      this::headObject),
                Map.entry("s3-crud:GetObject",                                       this::getObject),
                Map.entry("s3-crud:ListObjectsV2",                                   this::listObjectsV2),
                Map.entry("s3-crud:PutObjectMultipleKeys",                           this::putObjectMultipleKeys),
                Map.entry("s3-crud:ListObjectsV2Delimiter",                          this::listObjectsV2Delimiter),
                Map.entry("s3-crud:PutObjectFormContentType",                        this::putObjectFormContentType),
                Map.entry("s3-crud:PutObjectPlusInKey",                              this::putObjectPlusInKey),
                Map.entry("s3-crud:DeleteObject",                                    this::deleteObject),
                Map.entry("s3-crud:DeleteObjects",                                   this::deleteObjects),
                Map.entry("s3-crud:DeleteBucket",                                    this::deleteBucket),
                Map.entry("s3-copy:CreateSourceBucket",                              this::createSourceBucket),
                Map.entry("s3-copy:PutSourceObject",                                 this::putSourceObject),
                Map.entry("s3-copy:CopyObject",                                      this::copyObject),
                Map.entry("s3-multipart:CreateMultipartUpload",                      this::createMultipartUpload),
                Map.entry("s3-multipart:UploadPart",                                 this::uploadPart),
                Map.entry("s3-multipart:CompleteMultipartUpload",                    this::completeMultipartUpload),
                Map.entry("s3-multipart:AbortMultipartUpload",                       this::abortMultipartUpload),
                Map.entry("s3-versioning:PutBucketVersioning",                       this::putBucketVersioning),
                Map.entry("s3-versioning:GetBucketVersioning",                       this::getBucketVersioning),
                Map.entry("s3-tagging:PutObjectTagging",                             this::putObjectTagging),
                Map.entry("s3-tagging:GetObjectTagging",                             this::getObjectTagging),
                Map.entry("s3-tagging:PutBucketTagging",                             this::putBucketTagging),
                Map.entry("s3-tagging:GetBucketTagging",                             this::getBucketTagging),
                Map.entry("s3-website:PutBucketWebsite",                             this::putBucketWebsite),
                Map.entry("s3-website:GetBucketWebsite",                             this::getBucketWebsite),
                Map.entry("s3-cors:PutBucketCors",                                   this::putBucketCors),
                Map.entry("s3-cors:GetBucketCors",                                   this::getBucketCors),
                Map.entry("s3-lifecycle:PutBucketLifecycleConfiguration",            this::putBucketLifecycleConfiguration),
                Map.entry("s3-lifecycle:GetBucketLifecycleConfiguration",            this::getBucketLifecycleConfiguration),
                Map.entry("s3-lifecycle:DeleteBucketLifecycle",                      this::deleteBucketLifecycle),
                Map.entry("s3-lifecycle:GetBucketLifecycleConfigurationAfterDelete", this::getBucketLifecycleConfigurationAfterDelete)
        );
    }

    @Override
    public Map<String, TestFn> setups() {
        return Map.ofEntries(
                Map.entry("s3-crud",       this::setupCrud),
                Map.entry("s3-copy",       this::setupCopy),
                Map.entry("s3-multipart",  this::setupMultipart),
                Map.entry("s3-versioning", this::setupVersioning),
                Map.entry("s3-tagging",    this::setupTagging),
                Map.entry("s3-website",    this::setupWebsite),
                Map.entry("s3-cors",       this::setupCors),
                Map.entry("s3-lifecycle",  this::setupLifecycle)
        );
    }

    @Override
    public Map<String, TestFn> teardowns() {
        return Map.ofEntries(
                Map.entry("s3-crud",       ctx -> emptyAndDeleteBucket(ctx.getString("s3Bucket"))),
                Map.entry("s3-copy",       this::teardownCopy),
                Map.entry("s3-multipart",  ctx -> emptyAndDeleteBucket(ctx.getString("s3MpBucket"))),
                Map.entry("s3-versioning", ctx -> emptyAndDeleteBucket(ctx.getString("s3VerBucket"))),
                Map.entry("s3-tagging",    ctx -> emptyAndDeleteBucket(ctx.getString("s3TagBucket"))),
                Map.entry("s3-website",    ctx -> emptyAndDeleteBucket(ctx.getString("s3WebBucket"))),
                Map.entry("s3-cors",       ctx -> emptyAndDeleteBucket(ctx.getString("s3CorsBucket"))),
                Map.entry("s3-lifecycle",  this::teardownLifecycle)
        );
    }

    // ── s3-crud ────────────────────────────────────────────────────────────────

    private void setupCrud(TestContext ctx) throws Exception {
        String bucket = ctx.runId() + "-s3crud";
        s3().createBucket(r -> r.bucket(bucket));
        ctx.set("s3Bucket", bucket);
    }

    private void createBucket(TestContext ctx) throws Exception {
        String name = ctx.runId() + "-s3create";
        s3().createBucket(r -> r.bucket(name));
        try {
            var resp = s3().listBuckets();
            boolean found = resp.buckets().stream().anyMatch(b -> b.name().equals(name));
            Assertions.assertTrue(found, "CreateBucket: bucket " + name + " not found in listBuckets (runId=" + ctx.runId() + ")");
        } finally {
            emptyAndDeleteBucket(name);
        }
    }

    private void putObject(TestContext ctx) throws Exception {
        String bucket = ctx.getString("s3Bucket");
        s3().putObject(r -> r.bucket(bucket).key("test-key"),
                RequestBody.fromString("hello world"));
        var head = s3().headObject(r -> r.bucket(bucket).key("test-key"));
        Assertions.assertGreaterThan(0L, head.contentLength(), "PutObject: ContentLength should be > 0");
        ctx.set("s3Key", "test-key");
    }

    private void headObject(TestContext ctx) throws Exception {
        String bucket = ctx.getString("s3Bucket");
        var resp = s3().headObject(r -> r.bucket(bucket).key("test-key"));
        Assertions.assertNotNull(resp.contentLength(), "HeadObject: contentLength");
        Assertions.assertGreaterThan(0L, resp.contentLength(), "HeadObject: ContentLength should be > 0");
    }

    private void getObject(TestContext ctx) throws Exception {
        String bucket = ctx.getString("s3Bucket");
        var bytes = s3().getObject(
                r -> r.bucket(bucket).key("test-key"),
                ResponseTransformer.toBytes());
        String body = bytes.asString(StandardCharsets.UTF_8);
        Assertions.assertEquals("hello world", body, "GetObject: body mismatch");
    }

    private void listObjectsV2(TestContext ctx) throws Exception {
        String bucket = ctx.getString("s3Bucket");
        var resp = s3().listObjectsV2(r -> r.bucket(bucket));
        boolean found = resp.contents().stream().anyMatch(o -> o.key().equals("test-key"));
        Assertions.assertTrue(found, "ListObjectsV2: test-key not found (runId=" + ctx.runId() + ")");
    }

    private void putObjectMultipleKeys(TestContext ctx) throws Exception {
        String bucket = ctx.getString("s3Bucket");
        s3().putObject(r -> r.bucket(bucket).key("prefix/a"), RequestBody.fromString("a"));
        s3().putObject(r -> r.bucket(bucket).key("prefix/b"), RequestBody.fromString("b"));
        var resp = s3().listObjectsV2(r -> r.bucket(bucket).prefix("prefix/"));
        Assertions.assertGreaterThanOrEqual(2, resp.contents().size(),
                "PutObjectMultipleKeys: expected >= 2 objects under prefix/");
    }

    private void listObjectsV2Delimiter(TestContext ctx) throws Exception {
        String bucket = ctx.getString("s3Bucket");
        var resp = s3().listObjectsV2(r -> r.bucket(bucket).prefix("prefix/").delimiter("/"));
        Assertions.assertGreaterThanOrEqual(2, resp.contents().size(),
                "ListObjectsV2Delimiter: expected >= 2 objects under prefix/");
    }

    // Content-Type is metadata on AWS, not a parsing instruction. An emulator
    // that sniffs it to spot AWS Query traffic can consume the body before the
    // S3 handler sees it and silently store zero bytes. Every SDK picks a
    // sensible default type, so only application code that sets this one
    // explicitly reaches the case.
    private void putObjectFormContentType(TestContext ctx) throws Exception {
        String bucket = ctx.getString("s3Bucket");
        String key = "form-content-type";
        String body = "hello-body-check";
        s3().putObject(
                r -> r.bucket(bucket).key(key).contentType("application/x-www-form-urlencoded"),
                RequestBody.fromString(body));

        var stored = s3().getObject(r -> r.bucket(bucket).key(key), ResponseTransformer.toBytes());
        Assertions.assertEquals(body, stored.asString(StandardCharsets.UTF_8),
                "PutObjectFormContentType: stored body does not match what was sent");
        Assertions.assertEquals("application/x-www-form-urlencoded", stored.response().contentType(),
                "PutObjectFormContentType: ContentType was not preserved");

        // Leave the group bucket as it was found: some suites' DeleteBucket
        // test deletes this shared bucket and needs it empty by then.
        s3().deleteObject(r -> r.bucket(bucket).key(key));
    }

    // SDKs percent-encode "+" as %2B in the request path — unlike a space or a
    // multi-byte character, whose encodings survive a round trip through a
    // server's URL canonicalisation — so a server that reads the raw path
    // without decoding stores the literal "%2B" in the key.
    private void putObjectPlusInKey(TestContext ctx) throws Exception {
        String bucket = ctx.getString("s3Bucket");
        String key = "plusonly/a+b.txt";
        String body = "plus-body";
        s3().putObject(r -> r.bucket(bucket).key(key), RequestBody.fromString(body));

        var stored = s3().getObject(r -> r.bucket(bucket).key(key), ResponseTransformer.toBytes());
        Assertions.assertEquals(body, stored.asString(StandardCharsets.UTF_8),
                "PutObjectPlusInKey: could not read back " + key);

        var listed = s3().listObjectsV2(r -> r.bucket(bucket).prefix("plusonly/"));
        boolean found = listed.contents().stream().anyMatch(o -> o.key().equals(key));
        Assertions.assertTrue(found, "PutObjectPlusInKey: expected " + key + " in ListObjectsV2, got "
                + listed.contents().stream().map(software.amazon.awssdk.services.s3.model.S3Object::key).toList());

        // Leave the group bucket as it was found: some suites' DeleteBucket
        // test deletes this shared bucket and needs it empty by then.
        s3().deleteObject(r -> r.bucket(bucket).key(key));
    }

    private void deleteObject(TestContext ctx) throws Exception {
        String bucket = ctx.getString("s3Bucket");
        s3().deleteObject(r -> r.bucket(bucket).key("test-key"));
        var resp = s3().listObjectsV2(r -> r.bucket(bucket));
        boolean found = resp.contents().stream().anyMatch(o -> o.key().equals("test-key"));
        Assertions.assertFalse(found, "DeleteObject: test-key still present after deletion");
    }

    private void deleteObjects(TestContext ctx) throws Exception {
        String bucket = ctx.getString("s3Bucket");
        s3().putObject(r -> r.bucket(bucket).key("del/a"), RequestBody.fromString("a"));
        s3().putObject(r -> r.bucket(bucket).key("del/b"), RequestBody.fromString("b"));
        s3().deleteObjects(r -> r.bucket(bucket)
                .delete(d -> d.objects(
                        ObjectIdentifier.builder().key("del/a").build(),
                        ObjectIdentifier.builder().key("del/b").build())));
        var resp = s3().listObjectsV2(r -> r.bucket(bucket).prefix("del/"));
        Assertions.assertTrue(resp.contents().isEmpty(), "DeleteObjects: objects still present after batch delete");
    }

    private void deleteBucket(TestContext ctx) throws Exception {
        // The bucket created in setup will be deleted by teardown;
        // this test creates its own ephemeral bucket to verify the API.
        String name = ctx.runId() + "-s3del";
        s3().createBucket(r -> r.bucket(name));
        s3().deleteBucket(r -> r.bucket(name));
        var resp = s3().listBuckets();
        boolean found = resp.buckets().stream().anyMatch(b -> b.name().equals(name));
        Assertions.assertFalse(found, "DeleteBucket: bucket " + name + " still present after deletion");
    }

    // ── s3-copy ────────────────────────────────────────────────────────────────

    private void setupCopy(TestContext ctx) throws Exception {
        String src = ctx.runId() + "-s3copysrc";
        String dst = ctx.runId() + "-s3copydst";
        s3().createBucket(r -> r.bucket(src));
        s3().createBucket(r -> r.bucket(dst));
        ctx.set("s3CopySrc", src);
        ctx.set("s3CopyDst", dst);
    }

    private void teardownCopy(TestContext ctx) {
        emptyAndDeleteBucket(ctx.getString("s3CopySrc"));
        emptyAndDeleteBucket(ctx.getString("s3CopyDst"));
    }

    private void createSourceBucket(TestContext ctx) {
        // Bucket already created in setup; just assert it exists.
        String bucket = ctx.getString("s3CopySrc");
        Assertions.assertNotNull(bucket, "s3CopySrc");
    }

    private void putSourceObject(TestContext ctx) throws Exception {
        String bucket = ctx.getString("s3CopySrc");
        s3().putObject(r -> r.bucket(bucket).key("source.txt"),
                RequestBody.fromString("copy me"));
    }

    private void copyObject(TestContext ctx) throws Exception {
        String src = ctx.getString("s3CopySrc");
        String dst = ctx.getString("s3CopyDst");
        s3().copyObject(r -> r
                .sourceBucket(src).sourceKey("source.txt")
                .destinationBucket(dst).destinationKey("copied.txt"));
        var head = s3().headObject(r -> r.bucket(dst).key("copied.txt"));
        Assertions.assertNotNull(head.contentLength(), "CopyObject: destination object head returned null contentLength");
    }

    // ── s3-multipart ──────────────────────────────────────────────────────────

    private void setupMultipart(TestContext ctx) throws Exception {
        String bucket = ctx.runId() + "-s3mp";
        s3().createBucket(r -> r.bucket(bucket));
        ctx.set("s3MpBucket", bucket);
    }

    private void createMultipartUpload(TestContext ctx) throws Exception {
        String bucket = ctx.getString("s3MpBucket");
        var resp = s3().createMultipartUpload(r -> r.bucket(bucket).key("mp-key"));
        Assertions.assertNotBlank(resp.uploadId(), "createMultipartUpload: uploadId");
        ctx.set("s3UploadId", resp.uploadId());
    }

    private void uploadPart(TestContext ctx) throws Exception {
        String bucket   = ctx.getString("s3MpBucket");
        String uploadId = ctx.getString("s3UploadId");
        // S3 requires each part to be >= 5 MiB except the last part.
        // For the local emulator we use a smaller payload and treat 5 MiB
        // enforcement as an emulator detail, not a Java SDK issue.
        byte[] data = "A".repeat(5 * 1024 * 1024 + 1).getBytes(StandardCharsets.UTF_8);
        var resp = s3().uploadPart(r -> r
                .bucket(bucket).key("mp-key")
                .uploadId(uploadId)
                .partNumber(1),
                RequestBody.fromBytes(data));
        Assertions.assertNotBlank(resp.eTag(), "uploadPart: eTag");
        ctx.set("s3PartETag", resp.eTag());
    }

    private void completeMultipartUpload(TestContext ctx) throws Exception {
        String bucket   = ctx.getString("s3MpBucket");
        String uploadId = ctx.getString("s3UploadId");
        String eTag     = ctx.getString("s3PartETag");
        var completed = s3().completeMultipartUpload(r -> r
                .bucket(bucket).key("mp-key")
                .uploadId(uploadId)
                .multipartUpload(m -> m.parts(
                        CompletedPart.builder().partNumber(1).eTag(eTag).build())));
        var head = s3().headObject(r -> r.bucket(bucket).key("mp-key"));
        Assertions.assertGreaterThan(0L, head.contentLength(), "CompleteMultipartUpload: contentLength");
        // S3 gives a multipart object the MD5 of its parts' binary MD5s, suffixed with the part count (#2232):
        // md5(md5(5 MiB + 1 of "A"))-1 for uploadPart's single part.
        String want = "\"c746886354dadfa4806f4c2d6745c2c8-1\"";
        Assertions.assertEquals(want, completed.eTag(), "CompleteMultipartUpload: eTag");
        Assertions.assertEquals(want, head.eTag(), "CompleteMultipartUpload: headObject eTag");
    }

    private void abortMultipartUpload(TestContext ctx) throws Exception {
        String bucket = ctx.getString("s3MpBucket");
        // Start a fresh upload just to abort it.
        var resp = s3().createMultipartUpload(r -> r.bucket(bucket).key("abort-key"));
        s3().abortMultipartUpload(r -> r
                .bucket(bucket).key("abort-key")
                .uploadId(resp.uploadId()));
    }

    // ── s3-versioning ─────────────────────────────────────────────────────────

    private void setupVersioning(TestContext ctx) throws Exception {
        String bucket = ctx.runId() + "-s3ver";
        s3().createBucket(r -> r.bucket(bucket));
        ctx.set("s3VerBucket", bucket);
    }

    private void putBucketVersioning(TestContext ctx) throws Exception {
        String bucket = ctx.getString("s3VerBucket");
        s3().putBucketVersioning(r -> r.bucket(bucket)
                .versioningConfiguration(v -> v.status(BucketVersioningStatus.ENABLED)));
    }

    private void getBucketVersioning(TestContext ctx) throws Exception {
        String bucket = ctx.getString("s3VerBucket");
        var resp = s3().getBucketVersioning(r -> r.bucket(bucket));
        Assertions.assertEquals(BucketVersioningStatus.ENABLED, resp.status(),
                "GetBucketVersioning: status mismatch");
    }

    // ── s3-tagging ────────────────────────────────────────────────────────────

    private void setupTagging(TestContext ctx) throws Exception {
        String bucket = ctx.runId() + "-s3tag";
        s3().createBucket(r -> r.bucket(bucket));
        s3().putObject(r -> r.bucket(bucket).key("tagged-obj"), RequestBody.fromString("data"));
        ctx.set("s3TagBucket", bucket);
    }

    private void putObjectTagging(TestContext ctx) throws Exception {
        String bucket = ctx.getString("s3TagBucket");
        s3().putObjectTagging(r -> r.bucket(bucket).key("tagged-obj")
                .tagging(t -> t.tagSet(Tag.builder().key("env").value("test").build())));
    }

    private void getObjectTagging(TestContext ctx) throws Exception {
        String bucket = ctx.getString("s3TagBucket");
        var resp = s3().getObjectTagging(r -> r.bucket(bucket).key("tagged-obj"));
        boolean found = resp.tagSet().stream().anyMatch(t -> t.key().equals("env") && t.value().equals("test"));
        Assertions.assertTrue(found, "GetObjectTagging: env=test tag not found");
    }

    private void putBucketTagging(TestContext ctx) throws Exception {
        String bucket = ctx.getString("s3TagBucket");
        s3().putBucketTagging(r -> r.bucket(bucket)
                .tagging(t -> t.tagSet(Tag.builder().key("project").value("overcast").build())));
    }

    private void getBucketTagging(TestContext ctx) throws Exception {
        String bucket = ctx.getString("s3TagBucket");
        var resp = s3().getBucketTagging(r -> r.bucket(bucket));
        boolean found = resp.tagSet().stream().anyMatch(t -> t.key().equals("project"));
        Assertions.assertTrue(found, "GetBucketTagging: project tag not found");
    }

    // ── s3-website ────────────────────────────────────────────────────────────

    private void setupWebsite(TestContext ctx) throws Exception {
        String bucket = ctx.runId() + "-s3web";
        s3().createBucket(r -> r.bucket(bucket));
        ctx.set("s3WebBucket", bucket);
    }

    private void putBucketWebsite(TestContext ctx) throws Exception {
        String bucket = ctx.getString("s3WebBucket");
        s3().putBucketWebsite(r -> r.bucket(bucket)
                .websiteConfiguration(w -> w
                        .indexDocument(i -> i.suffix("index.html"))
                        .errorDocument(e -> e.key("error.html"))));
    }

    private void getBucketWebsite(TestContext ctx) throws Exception {
        String bucket = ctx.getString("s3WebBucket");
        var resp = s3().getBucketWebsite(r -> r.bucket(bucket));
        Assertions.assertEquals("index.html", resp.indexDocument().suffix(),
                "GetBucketWebsite: indexDocument suffix mismatch");
    }

    // ── s3-cors ───────────────────────────────────────────────────────────────

    private void setupCors(TestContext ctx) throws Exception {
        String bucket = ctx.runId() + "-s3cors";
        s3().createBucket(r -> r.bucket(bucket));
        ctx.set("s3CorsBucket", bucket);
    }

    private void putBucketCors(TestContext ctx) throws Exception {
        String bucket = ctx.getString("s3CorsBucket");
        s3().putBucketCors(r -> r.bucket(bucket)
                .corsConfiguration(c -> c.corsRules(
                        CORSRule.builder()
                                .allowedMethods("GET", "PUT")
                                .allowedOrigins("*")
                                .allowedHeaders("*")
                                .build())));
    }

    private void getBucketCors(TestContext ctx) throws Exception {
        String bucket = ctx.getString("s3CorsBucket");
        var resp = s3().getBucketCors(r -> r.bucket(bucket));
        Assertions.assertNotEmpty(resp.corsRules(), "GetBucketCors: no CORS rules returned");
    }

    // ── s3-lifecycle ──────────────────────────────────────────────────────────

    private void setupLifecycle(TestContext ctx) throws Exception {
        String bucket = ctx.runId() + "-s3lifecycle";
        s3().createBucket(r -> r.bucket(bucket));
        ctx.set("s3LifecycleBucket", bucket);
    }

    private void teardownLifecycle(TestContext ctx) {
        String bucket = ctx.getString("s3LifecycleBucket");
        if (bucket == null) return;
        try { s3().deleteBucketLifecycle(r -> r.bucket(bucket)); } catch (Exception ignored) {}
        emptyAndDeleteBucket(bucket);
    }

    /**
     * Returns the stored rule with the given ID. Matching on the ID rather than
     * taking the first rule back keeps the assertion about this group's own rule.
     */
    private LifecycleRule lifecycleRule(String bucket, String id) throws Exception {
        var resp = s3().getBucketLifecycleConfiguration(r -> r.bucket(bucket));
        for (LifecycleRule rule : resp.rules()) {
            if (id.equals(rule.id())) return rule;
        }
        throw new AssertionError("lifecycle rule " + id + " not found among " + resp.rules().size() + " rules");
    }

    private void putBucketLifecycleConfiguration(TestContext ctx) throws Exception {
        String bucket = ctx.getString("s3LifecycleBucket");
        s3().putBucketLifecycleConfiguration(r -> r.bucket(bucket)
                .lifecycleConfiguration(c -> c.rules(
                        LifecycleRule.builder()
                                .id("expire-logs")
                                .status(ExpirationStatus.ENABLED)
                                .filter(LifecycleRuleFilter.builder().prefix("logs/").build())
                                .expiration(LifecycleExpiration.builder().days(30).build())
                                .transitions(Transition.builder()
                                        .days(7)
                                        .storageClass(TransitionStorageClass.GLACIER)
                                        .build())
                                .build())));

        LifecycleRule rule = lifecycleRule(bucket, "expire-logs");
        Assertions.assertEquals(30, rule.expiration().days(),
                "PutBucketLifecycleConfiguration: expiration days mismatch");
    }

    private void getBucketLifecycleConfiguration(TestContext ctx) throws Exception {
        String bucket = ctx.getString("s3LifecycleBucket");
        LifecycleRule rule = lifecycleRule(bucket, "expire-logs");
        Assertions.assertEquals(ExpirationStatus.ENABLED, rule.status(),
                "GetBucketLifecycleConfiguration: status mismatch");
        Assertions.assertEquals("logs/", rule.filter().prefix(),
                "GetBucketLifecycleConfiguration: filter prefix mismatch");
        Assertions.assertNotEmpty(rule.transitions(),
                "GetBucketLifecycleConfiguration: no transitions returned");
        Assertions.assertEquals(TransitionStorageClass.GLACIER, rule.transitions().get(0).storageClass(),
                "GetBucketLifecycleConfiguration: transition storage class mismatch");
    }

    private void deleteBucketLifecycle(TestContext ctx) throws Exception {
        String bucket = ctx.getString("s3LifecycleBucket");
        s3().deleteBucketLifecycle(r -> r.bucket(bucket));
    }

    private void getBucketLifecycleConfigurationAfterDelete(TestContext ctx) throws Exception {
        String bucket = ctx.getString("s3LifecycleBucket");
        try {
            var resp = s3().getBucketLifecycleConfiguration(r -> r.bucket(bucket));
            throw new AssertionError(
                    "GetBucketLifecycleConfigurationAfterDelete: expected an error, got "
                            + resp.rules().size() + " rules");
        } catch (S3Exception e) {
            String code = e.awsErrorDetails() == null ? "" : e.awsErrorDetails().errorCode();
            Assertions.assertEquals("NoSuchLifecycleConfiguration", code,
                    "GetBucketLifecycleConfigurationAfterDelete: unexpected error code");
        }
    }

    // ── Helpers ───────────────────────────────────────────────────────────────

    private void emptyAndDeleteBucket(String bucket) {
        if (bucket == null) return;
        try {
            // Abort incomplete multipart uploads.
            var mp = s3().listMultipartUploads(r -> r.bucket(bucket));
            for (var u : mp.uploads()) {
                try {
                    s3().abortMultipartUpload(r -> r.bucket(bucket).key(u.key()).uploadId(u.uploadId()));
                } catch (Exception ignored) {}
            }
            // Delete all object versions and delete markers.
            var versions = s3().listObjectVersions(r -> r.bucket(bucket));
            for (var v : versions.versions()) {
                try { s3().deleteObject(r -> r.bucket(bucket).key(v.key()).versionId(v.versionId())); }
                catch (Exception ignored) {}
            }
            for (var dm : versions.deleteMarkers()) {
                try { s3().deleteObject(r -> r.bucket(bucket).key(dm.key()).versionId(dm.versionId())); }
                catch (Exception ignored) {}
            }
            // Delete remaining current objects.
            var objs = s3().listObjectsV2(r -> r.bucket(bucket));
            for (var obj : objs.contents()) {
                try { s3().deleteObject(r -> r.bucket(bucket).key(obj.key())); }
                catch (Exception ignored) {}
            }
            s3().deleteBucket(r -> r.bucket(bucket));
        } catch (Exception ignored) {}
    }
}
