/**
 * groups/s3.ts — S3 compatibility test groups for the Node.js suite.
 *
 * Groups:
 *   s3-crud       — bucket + object lifecycle (implemented)
 *   s3-multipart  — multipart upload (implemented)
 *   s3-copy       — CopyObject (implemented)
 *   s3-versions   — bucket versioning (TODO: P2)
 *   s3-tagging    — object + bucket tagging (TODO: P2)
 *   s3-website    — static website config (not implemented)
 *   s3-cors       — CORS configuration (not implemented)
 *   s3-lifecycle  — bucket lifecycle rules
 */

import {
  CreateBucketCommand,
  DeleteBucketCommand,
  ListBucketsCommand,
  PutObjectCommand,
  GetObjectCommand,
  HeadObjectCommand,
  DeleteObjectCommand,
  ListObjectsV2Command,
  ListMultipartUploadsCommand,
  DeleteObjectsCommand,
  CopyObjectCommand,
  CreateMultipartUploadCommand,
  UploadPartCommand,
  CompleteMultipartUploadCommand,
  AbortMultipartUploadCommand,
  PutBucketVersioningCommand,
  GetBucketVersioningCommand,
  PutObjectTaggingCommand,
  GetObjectTaggingCommand,
  PutBucketTaggingCommand,
  GetBucketTaggingCommand,
  PutBucketWebsiteCommand,
  GetBucketWebsiteCommand,
  PutBucketCorsCommand,
  GetBucketCorsCommand,
  PutBucketLifecycleConfigurationCommand,
  GetBucketLifecycleConfigurationCommand,
  DeleteBucketLifecycleCommand,
} from "@aws-sdk/client-s3";
import { makeClients } from "../lib/clients.ts";
import type { TestGroup } from "../lib/harness.ts";
import * as assert from "node:assert/strict";

export function makeS3Groups(suite: string): TestGroup[] {
  return [
    // ── s3-crud ────────────────────────────────────────────────────────────
    {
      suite,
      service: "s3",
      name: "s3-crud",
      tests: [
        {
          name: "CreateBucket",
          fn: async (ctx) => {
            const { s3 } = makeClients(ctx);
            // Use a dedicated bucket so this test doesn't conflict with the
            // group setup, which already creates ${runId}-s3-crud.
            const bucket = `${ctx.runId}-s3-create`;
            await s3.send(new CreateBucketCommand({ Bucket: bucket }));
            try {
              const { Buckets = [] } = await s3.send(
                new ListBucketsCommand({}),
              );
              assert.ok(
                Buckets.some((b) => b.Name === bucket),
                `bucket ${bucket} not found after CreateBucket`,
              );
            } finally {
              await s3
                .send(new DeleteBucketCommand({ Bucket: bucket }))
                .catch(() => {});
            }
          },
        },
        {
          name: "PutObject",
          fn: async (ctx) => {
            const { s3 } = makeClients(ctx);
            const bucket = `${ctx.runId}-s3-crud`;
            const resp = await s3.send(
              new PutObjectCommand({
                Bucket: bucket,
                Key: "hello.txt",
                Body: "hello world",
              }),
            );
            assert.ok(resp.ETag, "PutObject: missing ETag in response");
          },
        },
        {
          name: "HeadObject",
          fn: async (ctx) => {
            const { s3 } = makeClients(ctx);
            const bucket = `${ctx.runId}-s3-crud`;
            const resp = await s3.send(
              new HeadObjectCommand({ Bucket: bucket, Key: "hello.txt" }),
            );
            assert.ok(resp.ContentLength, "HeadObject: missing ContentLength");
          },
        },
        {
          name: "GetObject",
          fn: async (ctx) => {
            const { s3 } = makeClients(ctx);
            const bucket = `${ctx.runId}-s3-crud`;
            const resp = await s3.send(
              new GetObjectCommand({ Bucket: bucket, Key: "hello.txt" }),
            );
            const body = await resp.Body?.transformToString();
            assert.strictEqual(
              body,
              "hello world",
              `GetObject: expected "hello world", got ${JSON.stringify(body)}`,
            );
          },
        },
        {
          name: "ListObjectsV2",
          fn: async (ctx) => {
            const { s3 } = makeClients(ctx);
            const bucket = `${ctx.runId}-s3-crud`;
            const resp = await s3.send(
              new ListObjectsV2Command({ Bucket: bucket }),
            );
            assert.ok(
              resp.Contents?.some((o) => o.Key === "hello.txt"),
              "ListObjectsV2: hello.txt not found",
            );
          },
        },
        {
          name: "PutObjectMultipleKeys",
          op: "PutObject",
          fn: async (ctx) => {
            const { s3 } = makeClients(ctx);
            const bucket = `${ctx.runId}-s3-crud`;
            await Promise.all([
              s3.send(
                new PutObjectCommand({
                  Bucket: bucket,
                  Key: "dir/a.txt",
                  Body: "a",
                }),
              ),
              s3.send(
                new PutObjectCommand({
                  Bucket: bucket,
                  Key: "dir/b.txt",
                  Body: "b",
                }),
              ),
            ]);
            const resp = await s3.send(
              new ListObjectsV2Command({ Bucket: bucket, Prefix: "dir/" }),
            );
            assert.ok(
              (resp.KeyCount ?? 0) >= 2,
              `ListObjectsV2 with prefix: expected >=2 keys, got ${resp.KeyCount}`,
            );
          },
        },
        {
          name: "ListObjectsV2Delimiter",
          op: "ListObjectsV2",
          fn: async (ctx) => {
            const { s3 } = makeClients(ctx);
            const bucket = `${ctx.runId}-s3-crud`;
            const resp = await s3.send(
              new ListObjectsV2Command({ Bucket: bucket, Delimiter: "/" }),
            );
            assert.ok(
              resp.CommonPrefixes?.some((p) => p.Prefix === "dir/"),
              "ListObjectsV2 with delimiter: expected dir/ in CommonPrefixes",
            );
          },
        },
        {
          // Content-Type is metadata on AWS, not a parsing instruction. An
          // emulator that sniffs it to spot AWS Query traffic can consume the
          // body before the S3 handler sees it and silently store zero bytes.
          // Every SDK picks a sensible default type, so only application code
          // that sets this one explicitly reaches the case.
          name: "PutObjectFormContentType",
          op: "PutObject",
          fn: async (ctx) => {
            const { s3 } = makeClients(ctx);
            const bucket = `${ctx.runId}-s3-crud`;
            const body = "hello-body-check";
            await s3.send(
              new PutObjectCommand({
                Bucket: bucket,
                Key: "form-content-type",
                Body: body,
                ContentType: "application/x-www-form-urlencoded",
              }),
            );
            const resp = await s3.send(
              new GetObjectCommand({
                Bucket: bucket,
                Key: "form-content-type",
              }),
            );
            assert.strictEqual(
              await resp.Body?.transformToString(),
              body,
              "PutObjectFormContentType: stored body does not match what was sent",
            );
            assert.strictEqual(
              resp.ContentType,
              "application/x-www-form-urlencoded",
              "PutObjectFormContentType: ContentType was not preserved",
            );
            // Leave the group bucket as it was found — DeleteBucket below
            // deletes this shared bucket and needs it empty by then.
            await s3.send(
              new DeleteObjectCommand({
                Bucket: bucket,
                Key: "form-content-type",
              }),
            );
          },
        },
        {
          // SDKs percent-encode "+" as %2B in the request path — unlike a space
          // or a multi-byte character, whose encodings survive a round trip
          // through a server's URL canonicalisation — so a server that reads
          // the raw path without decoding stores the literal "%2B" in the key.
          name: "PutObjectPlusInKey",
          op: "PutObject",
          fn: async (ctx) => {
            const { s3 } = makeClients(ctx);
            const bucket = `${ctx.runId}-s3-crud`;
            const key = "plusonly/a+b.txt";
            const body = "plus-body";
            await s3.send(
              new PutObjectCommand({ Bucket: bucket, Key: key, Body: body }),
            );
            const resp = await s3.send(
              new GetObjectCommand({ Bucket: bucket, Key: key }),
            );
            assert.strictEqual(
              await resp.Body?.transformToString(),
              body,
              `PutObjectPlusInKey: could not read back ${key}`,
            );
            const listed = await s3.send(
              new ListObjectsV2Command({ Bucket: bucket, Prefix: "plusonly/" }),
            );
            const keys = (listed.Contents ?? []).map((o) => o.Key);
            assert.ok(
              keys.includes(key),
              `PutObjectPlusInKey: expected ${key} in ListObjectsV2, got ${JSON.stringify(keys)}`,
            );
            // Leave the group bucket as it was found — DeleteBucket below
            // deletes this shared bucket and needs it empty by then.
            await s3.send(new DeleteObjectCommand({ Bucket: bucket, Key: key }));
          },
        },
        {
          name: "DeleteObject",
          fn: async (ctx) => {
            const { s3 } = makeClients(ctx);
            const bucket = `${ctx.runId}-s3-crud`;
            await s3.send(
              new DeleteObjectCommand({ Bucket: bucket, Key: "hello.txt" }),
            );
            const listed = await s3.send(
              new ListObjectsV2Command({ Bucket: bucket }),
            );
            assert.notStrictEqual(
              (listed.Contents ?? []).some((o) => o.Key, "hello.txt"),
              "DeleteObject: hello.txt still present after delete",
            );
          },
        },
        {
          name: "DeleteObjects",
          fn: async (ctx) => {
            const { s3 } = makeClients(ctx);
            const bucket = `${ctx.runId}-s3-crud`;
            const resp = await s3.send(
              new DeleteObjectsCommand({
                Bucket: bucket,
                Delete: {
                  Objects: [{ Key: "dir/a.txt" }, { Key: "dir/b.txt" }],
                  Quiet: false,
                },
              }),
            );
            assert.ok(
              !resp.Errors?.length,
              `DeleteObjects reported errors: ${JSON.stringify(resp.Errors)}`,
            );
            const listed = await s3.send(
              new ListObjectsV2Command({ Bucket: bucket, Prefix: "dir/" }),
            );
            assert.ok(
              (listed.Contents ?? []).length <= 0,
              "DeleteObjects: objects still present after batch delete",
            );
          },
        },
        {
          name: "DeleteBucket",
          fn: async (ctx) => {
            const { s3 } = makeClients(ctx);
            const bucket = `${ctx.runId}-s3-crud`;
            await s3.send(new DeleteBucketCommand({ Bucket: bucket }));
            const { Buckets = [] } = await s3.send(new ListBucketsCommand({}));
            assert.ok(
              !Buckets.some((b) => b.Name === bucket),
              `bucket ${bucket} still present after DeleteBucket`,
            );
          },
        },
      ],
      setup: async (ctx) => {
        const { s3 } = makeClients(ctx);
        await s3.send(
          new CreateBucketCommand({ Bucket: `${ctx.runId}-s3-crud` }),
        );
      },
      teardown: async (ctx) => {
        const { s3 } = makeClients(ctx);
        const bucket = `${ctx.runId}-s3-crud`;
        // Purge all objects then delete bucket
        try {
          const listed = await s3.send(
            new ListObjectsV2Command({ Bucket: bucket }),
          );
          const keys = (listed.Contents ?? []).map((o) => ({ Key: o.Key! }));
          if (keys.length > 0) {
            await s3.send(
              new DeleteObjectsCommand({
                Bucket: bucket,
                Delete: { Objects: keys },
              }),
            );
          }
          await s3.send(new DeleteBucketCommand({ Bucket: bucket }));
        } catch {}
      },
    },

    // ── s3-copy ────────────────────────────────────────────────────────────
    {
      suite,
      service: "s3",
      name: "s3-copy",
      tests: [
        {
          name: "CreateSourceBucket",
          op: false,
          fn: async (ctx) => {
            const { s3 } = makeClients(ctx);
            await s3.send(
              new CreateBucketCommand({ Bucket: `${ctx.runId}-s3-copy-src` }),
            );
            await s3.send(
              new CreateBucketCommand({ Bucket: `${ctx.runId}-s3-copy-dst` }),
            );
          },
        },
        {
          name: "PutSourceObject",
          fn: async (ctx) => {
            const { s3 } = makeClients(ctx);
            await s3.send(
              new PutObjectCommand({
                Bucket: `${ctx.runId}-s3-copy-src`,
                Key: "original.txt",
                Body: "copy me",
              }),
            );
          },
        },
        {
          name: "CopyObject",
          fn: async (ctx) => {
            const { s3 } = makeClients(ctx);
            const src = `${ctx.runId}-s3-copy-src`;
            const dst = `${ctx.runId}-s3-copy-dst`;
            await s3.send(
              new CopyObjectCommand({
                Bucket: dst,
                Key: "copied.txt",
                CopySource: `${src}/original.txt`,
              }),
            );
            const resp = await s3.send(
              new GetObjectCommand({ Bucket: dst, Key: "copied.txt" }),
            );
            const body = await resp.Body?.transformToString();
            assert.strictEqual(
              body,
              "copy me",
              `CopyObject: body mismatch: ${body}`,
            );
          },
        },
      ],
      teardown: async (ctx) => {
        const { s3 } = makeClients(ctx);
        for (const bucket of [
          `${ctx.runId}-s3-copy-src`,
          `${ctx.runId}-s3-copy-dst`,
        ]) {
          try {
            const listed = await s3.send(
              new ListObjectsV2Command({ Bucket: bucket }),
            );
            const keys = (listed.Contents ?? []).map((o) => ({ Key: o.Key! }));
            if (keys.length > 0) {
              await s3.send(
                new DeleteObjectsCommand({
                  Bucket: bucket,
                  Delete: { Objects: keys },
                }),
              );
            }
            await s3.send(new DeleteBucketCommand({ Bucket: bucket }));
          } catch {}
        }
      },
    },

    // ── s3-multipart ───────────────────────────────────────────────────────
    {
      suite,
      service: "s3",
      name: "s3-multipart",
      tests: [
        {
          name: "CreateMultipartUpload",
          fn: async (ctx) => {
            const { s3 } = makeClients(ctx);
            const bucket = `${ctx.runId}-s3-mp`;
            const resp = await s3.send(
              new CreateMultipartUploadCommand({
                Bucket: bucket,
                Key: "big.bin",
              }),
            );
            assert.ok(resp.UploadId, "CreateMultipartUpload: missing UploadId");
            // Store for subsequent tests via a side-channel in teardown context
            ctx["_mpUploadId"] = resp.UploadId;
          },
        },
        {
          name: "UploadPart",
          fn: async (ctx) => {
            const { s3 } = makeClients(ctx);
            const bucket = `${ctx.runId}-s3-mp`;
            const uploadId = ctx["_mpUploadId"] as string;
            assert.ok(uploadId, "no UploadId from previous step");
            // Minimum part size is 5 MiB except for the last part; use a small body here
            // since we're testing the API not the size limit
            const resp = await s3.send(
              new UploadPartCommand({
                Bucket: bucket,
                Key: "big.bin",
                PartNumber: 1,
                UploadId: uploadId,
                Body: "a".repeat(5 * 1024 * 1024), // 5 MiB
              }),
            );
            assert.ok(resp.ETag, "UploadPart: missing ETag");
            ctx["_mpETag"] = resp.ETag;
          },
        },
        {
          name: "CompleteMultipartUpload",
          fn: async (ctx) => {
            const { s3 } = makeClients(ctx);
            const bucket = `${ctx.runId}-s3-mp`;
            const uploadId = ctx["_mpUploadId"] as string;
            const etag = ctx["_mpETag"] as string;
            assert.ok(
              uploadId || !etag,
              "missing UploadId or ETag from previous steps",
            );
            const completed = await s3.send(
              new CompleteMultipartUploadCommand({
                Bucket: bucket,
                Key: "big.bin",
                UploadId: uploadId,
                MultipartUpload: { Parts: [{ PartNumber: 1, ETag: etag }] },
              }),
            );
            const head = await s3.send(
              new HeadObjectCommand({ Bucket: bucket, Key: "big.bin" }),
            );
            assert.ok(
              head.ContentLength,
              "CompleteMultipartUpload: missing ContentLength on assembled object",
            );
            // S3 gives a multipart object the MD5 of its parts' binary MD5s, suffixed with the part count (#2232):
            // md5(md5(5 MiB of "a"))-1 for UploadPart's single part.
            const want = '"65d79814053817eae59f7c7cee98d3f8-1"';
            assert.strictEqual(
              completed.ETag,
              want,
              "CompleteMultipartUpload: ETag",
            );
            assert.strictEqual(
              head.ETag,
              want,
              "CompleteMultipartUpload: HeadObject ETag",
            );
          },
        },
        {
          name: "AbortMultipartUpload",
          fn: async (ctx) => {
            const { s3 } = makeClients(ctx);
            const bucket = `${ctx.runId}-s3-mp`;
            // Create a new multipart upload and abort it
            const { UploadId } = await s3.send(
              new CreateMultipartUploadCommand({
                Bucket: bucket,
                Key: "aborted.bin",
              }),
            );
            await s3.send(
              new AbortMultipartUploadCommand({
                Bucket: bucket,
                Key: "aborted.bin",
                UploadId: UploadId!,
              }),
            );
            const listed = await s3.send(
              new ListMultipartUploadsCommand({ Bucket: bucket }),
            );
            assert.notStrictEqual(
              (listed.Uploads ?? []).some((u) => u.UploadId, UploadId),
              `AbortMultipartUpload: upload ${UploadId} still present after abort`,
            );
          },
        },
      ],
      setup: async (ctx) => {
        const { s3 } = makeClients(ctx);
        await s3.send(
          new CreateBucketCommand({ Bucket: `${ctx.runId}-s3-mp` }),
        );
      },
      teardown: async (ctx) => {
        const { s3 } = makeClients(ctx);
        const bucket = `${ctx.runId}-s3-mp`;
        // Abort any incomplete multipart uploads (not visible to ListObjectsV2)
        try {
          const uploads = await s3.send(
            new ListMultipartUploadsCommand({ Bucket: bucket }),
          );
          for (const upload of uploads.Uploads ?? []) {
            try {
              await s3.send(
                new AbortMultipartUploadCommand({
                  Bucket: bucket,
                  Key: upload.Key!,
                  UploadId: upload.UploadId!,
                }),
              );
            } catch {}
          }
        } catch {}
        try {
          const listed = await s3.send(
            new ListObjectsV2Command({ Bucket: bucket }),
          );
          const keys = (listed.Contents ?? []).map((o) => ({ Key: o.Key! }));
          if (keys.length > 0) {
            await s3.send(
              new DeleteObjectsCommand({
                Bucket: bucket,
                Delete: { Objects: keys },
              }),
            );
          }
          await s3.send(new DeleteBucketCommand({ Bucket: bucket }));
        } catch {}
      },
    },

    // ── s3-versioning ──────────────────────────────────────────────────────
    {
      suite,
      service: "s3",
      name: "s3-versioning",
      tests: [
        {
          name: "PutBucketVersioning",
          fn: async (ctx) => {
            const { s3 } = makeClients(ctx);
            const bucket = `${ctx.runId}-s3-ver`;
            await s3.send(
              new PutBucketVersioningCommand({
                Bucket: bucket,
                VersioningConfiguration: { Status: "Enabled" },
              }),
            );
            const resp = await s3.send(
              new GetBucketVersioningCommand({ Bucket: bucket }),
            );
            assert.strictEqual(
              resp.Status,
              "Enabled",
              `PutBucketVersioning: expected Enabled, got ${resp.Status}`,
            );
          },
        },
        {
          name: "GetBucketVersioning",
          fn: async (ctx) => {
            const { s3 } = makeClients(ctx);
            const bucket = `${ctx.runId}-s3-ver`;
            const resp = await s3.send(
              new GetBucketVersioningCommand({ Bucket: bucket }),
            );
            assert.strictEqual(
              resp.Status,
              "Enabled",
              `GetBucketVersioning: expected Enabled, got ${resp.Status}`,
            );
          },
        },
      ],
      setup: async (ctx) => {
        const { s3 } = makeClients(ctx);
        try {
          await s3.send(
            new CreateBucketCommand({ Bucket: `${ctx.runId}-s3-ver` }),
          );
        } catch (e: unknown) {
          // Bucket already exists from a previous failed run — that's fine,
          // teardown will clean it up at the end.
          const code =
            (e as { Code?: string; name?: string }).Code ??
            (e as { name?: string }).name;
          if (
            code !== "BucketAlreadyOwnedByYou" &&
            code !== "BucketAlreadyExists"
          )
            throw e;
        }
      },
      teardown: async (ctx) => {
        const { s3 } = makeClients(ctx);
        const bucket = `${ctx.runId}-s3-ver`;
        try {
          const listed = await s3.send(
            new ListObjectsV2Command({ Bucket: bucket }),
          );
          const keys = (listed.Contents ?? []).map((o) => ({ Key: o.Key! }));
          if (keys.length > 0) {
            await s3.send(
              new DeleteObjectsCommand({
                Bucket: bucket,
                Delete: { Objects: keys },
              }),
            );
          }
          await s3.send(new DeleteBucketCommand({ Bucket: bucket }));
        } catch {}
      },
    },

    // ── s3-tagging ─────────────────────────────────────────────────────────
    {
      suite,
      service: "s3",
      name: "s3-tagging",
      tests: [
        {
          name: "PutObjectTagging",
          fn: async (ctx) => {
            const { s3 } = makeClients(ctx);
            const bucket = `${ctx.runId}-s3-tag`;
            await s3.send(
              new PutObjectTaggingCommand({
                Bucket: bucket,
                Key: "obj.txt",
                Tagging: { TagSet: [{ Key: "env", Value: "test" }] },
              }),
            );
            const resp = await s3.send(
              new GetObjectTaggingCommand({ Bucket: bucket, Key: "obj.txt" }),
            );
            if (
              !resp.TagSet?.some((t) => t.Key === "env" && t.Value === "test")
            ) {
              throw new Error("PutObjectTagging: expected env=test tag");
            }
          },
        },
        {
          name: "GetObjectTagging",
          fn: async (ctx) => {
            const { s3 } = makeClients(ctx);
            const bucket = `${ctx.runId}-s3-tag`;
            const resp = await s3.send(
              new GetObjectTaggingCommand({ Bucket: bucket, Key: "obj.txt" }),
            );
            if (
              !resp.TagSet?.some((t) => t.Key === "env" && t.Value === "test")
            ) {
              throw new Error("GetObjectTagging: expected env=test tag");
            }
          },
        },
        {
          name: "PutBucketTagging",
          fn: async (ctx) => {
            const { s3 } = makeClients(ctx);
            const bucket = `${ctx.runId}-s3-tag`;
            await s3.send(
              new PutBucketTaggingCommand({
                Bucket: bucket,
                Tagging: { TagSet: [{ Key: "project", Value: "overcast" }] },
              }),
            );
            const resp = await s3.send(
              new GetBucketTaggingCommand({ Bucket: bucket }),
            );
            if (
              !resp.TagSet?.some(
                (t) => t.Key === "project" && t.Value === "overcast",
              )
            ) {
              throw new Error(
                "PutBucketTagging: expected project=overcast tag",
              );
            }
          },
        },
        {
          name: "GetBucketTagging",
          fn: async (ctx) => {
            const { s3 } = makeClients(ctx);
            const bucket = `${ctx.runId}-s3-tag`;
            const resp = await s3.send(
              new GetBucketTaggingCommand({ Bucket: bucket }),
            );
            if (
              !resp.TagSet?.some(
                (t) => t.Key === "project" && t.Value === "overcast",
              )
            ) {
              throw new Error(
                "GetBucketTagging: expected project=overcast tag",
              );
            }
          },
        },
      ],
      setup: async (ctx) => {
        const { s3 } = makeClients(ctx);
        const bucket = `${ctx.runId}-s3-tag`;
        try {
          await s3.send(new CreateBucketCommand({ Bucket: bucket }));
        } catch (e: unknown) {
          // Bucket already exists from a previous failed run — that's fine,
          // teardown will clean it up at the end.
          const code =
            (e as { Code?: string; name?: string }).Code ??
            (e as { name?: string }).name;
          if (
            code !== "BucketAlreadyOwnedByYou" &&
            code !== "BucketAlreadyExists"
          )
            throw e;
        }
        await s3.send(
          new PutObjectCommand({
            Bucket: bucket,
            Key: "obj.txt",
            Body: "tagged",
          }),
        );
      },
      teardown: async (ctx) => {
        const { s3 } = makeClients(ctx);
        const bucket = `${ctx.runId}-s3-tag`;
        try {
          const listed = await s3.send(
            new ListObjectsV2Command({ Bucket: bucket }),
          );
          const keys = (listed.Contents ?? []).map((o) => ({ Key: o.Key! }));
          if (keys.length > 0) {
            await s3.send(
              new DeleteObjectsCommand({
                Bucket: bucket,
                Delete: { Objects: keys },
              }),
            );
          }
          await s3.send(new DeleteBucketCommand({ Bucket: bucket }));
        } catch {}
      },
    },

    // ── s3-website ─────────────────────────────────────────────────────────
    {
      suite,
      service: "s3",
      name: "s3-website",
      tests: [
        {
          name: "PutBucketWebsite",
          fn: async (ctx) => {
            const { s3 } = makeClients(ctx);
            const bucket = `${ctx.runId}-s3-web`;
            await s3.send(
              new PutBucketWebsiteCommand({
                Bucket: bucket,
                WebsiteConfiguration: {
                  IndexDocument: { Suffix: "index.html" },
                  ErrorDocument: { Key: "error.html" },
                },
              }),
            );
          },
        },
        {
          name: "GetBucketWebsite",
          fn: async (ctx) => {
            const { s3 } = makeClients(ctx);
            const bucket = `${ctx.runId}-s3-web`;
            const resp = await s3.send(
              new GetBucketWebsiteCommand({ Bucket: bucket }),
            );
            assert.strictEqual(
              resp.IndexDocument?.Suffix,
              "index.html",
              `GetBucketWebsite: expected index.html, got ${resp.IndexDocument?.Suffix}`,
            );
          },
        },
      ],
      setup: async (ctx) => {
        const { s3 } = makeClients(ctx);
        await s3.send(
          new CreateBucketCommand({ Bucket: `${ctx.runId}-s3-web` }),
        );
      },
      teardown: async (ctx) => {
        const { s3 } = makeClients(ctx);
        try {
          await s3.send(
            new DeleteBucketCommand({ Bucket: `${ctx.runId}-s3-web` }),
          );
        } catch {}
      },
    },

    // ── s3-cors ────────────────────────────────────────────────────────────
    {
      suite,
      service: "s3",
      name: "s3-cors",
      tests: [
        {
          name: "PutBucketCors",
          fn: async (ctx) => {
            const { s3 } = makeClients(ctx);
            const bucket = `${ctx.runId}-s3-cors`;
            await s3.send(
              new PutBucketCorsCommand({
                Bucket: bucket,
                CORSConfiguration: {
                  CORSRules: [
                    {
                      AllowedHeaders: ["*"],
                      AllowedMethods: ["GET", "PUT"],
                      AllowedOrigins: ["https://example.com"],
                      MaxAgeSeconds: 3000,
                    },
                  ],
                },
              }),
            );
          },
        },
        {
          name: "GetBucketCors",
          fn: async (ctx) => {
            const { s3 } = makeClients(ctx);
            const bucket = `${ctx.runId}-s3-cors`;
            const resp = await s3.send(
              new GetBucketCorsCommand({ Bucket: bucket }),
            );
            assert.ok(
              resp.CORSRules?.length,
              "GetBucketCors: expected at least one rule",
            );
          },
        },
      ],
      setup: async (ctx) => {
        const { s3 } = makeClients(ctx);
        await s3.send(
          new CreateBucketCommand({ Bucket: `${ctx.runId}-s3-cors` }),
        );
      },
      teardown: async (ctx) => {
        const { s3 } = makeClients(ctx);
        try {
          await s3.send(
            new DeleteBucketCommand({ Bucket: `${ctx.runId}-s3-cors` }),
          );
        } catch {}
      },
    },

    // ── s3-lifecycle ───────────────────────────────────────────────────────
    {
      suite,
      service: "s3",
      name: "s3-lifecycle",
      tests: [
        {
          name: "PutBucketLifecycleConfiguration",
          fn: async (ctx) => {
            const { s3 } = makeClients(ctx);
            const bucket = `${ctx.runId}-s3-lifecycle`;
            await s3.send(
              new PutBucketLifecycleConfigurationCommand({
                Bucket: bucket,
                LifecycleConfiguration: {
                  Rules: [
                    {
                      ID: "expire-logs",
                      Status: "Enabled",
                      Filter: { Prefix: "logs/" },
                      Expiration: { Days: 30 },
                      Transitions: [{ Days: 7, StorageClass: "GLACIER" }],
                    },
                  ],
                },
              }),
            );
            const resp = await s3.send(
              new GetBucketLifecycleConfigurationCommand({ Bucket: bucket }),
            );
            const rule = (resp.Rules ?? []).find((r) => r.ID === "expire-logs");
            assert.ok(
              rule,
              `PutBucketLifecycleConfiguration: rule expire-logs missing from ${JSON.stringify(resp.Rules)}`,
            );
            assert.equal(rule.Expiration?.Days, 30);
          },
        },
        {
          name: "GetBucketLifecycleConfiguration",
          fn: async (ctx) => {
            const { s3 } = makeClients(ctx);
            const bucket = `${ctx.runId}-s3-lifecycle`;
            const resp = await s3.send(
              new GetBucketLifecycleConfigurationCommand({ Bucket: bucket }),
            );
            const rule = (resp.Rules ?? []).find((r) => r.ID === "expire-logs");
            assert.ok(
              rule,
              `GetBucketLifecycleConfiguration: rule expire-logs missing from ${JSON.stringify(resp.Rules)}`,
            );
            assert.equal(rule.Status, "Enabled");
            assert.equal(rule.Filter?.Prefix, "logs/");
            assert.equal(rule.Transitions?.[0]?.StorageClass, "GLACIER");
          },
        },
        {
          name: "DeleteBucketLifecycle",
          fn: async (ctx) => {
            const { s3 } = makeClients(ctx);
            await s3.send(
              new DeleteBucketLifecycleCommand({
                Bucket: `${ctx.runId}-s3-lifecycle`,
              }),
            );
          },
        },
        {
          name: "GetBucketLifecycleConfigurationAfterDelete",
          fn: async (ctx) => {
            const { s3 } = makeClients(ctx);
            const bucket = `${ctx.runId}-s3-lifecycle`;
            await assert.rejects(
              () =>
                s3.send(
                  new GetBucketLifecycleConfigurationCommand({
                    Bucket: bucket,
                  }),
                ),
              (err: { name?: string }) =>
                err.name === "NoSuchLifecycleConfiguration",
              "GetBucketLifecycleConfigurationAfterDelete: expected NoSuchLifecycleConfiguration",
            );
          },
        },
      ],
      setup: async (ctx) => {
        const { s3 } = makeClients(ctx);
        await s3.send(
          new CreateBucketCommand({ Bucket: `${ctx.runId}-s3-lifecycle` }),
        );
      },
      teardown: async (ctx) => {
        const { s3 } = makeClients(ctx);
        try {
          await s3.send(
            new DeleteBucketLifecycleCommand({
              Bucket: `${ctx.runId}-s3-lifecycle`,
            }),
          );
        } catch {}
        try {
          await s3.send(
            new DeleteBucketCommand({ Bucket: `${ctx.runId}-s3-lifecycle` }),
          );
        } catch {}
      },
    },
  ];
}
