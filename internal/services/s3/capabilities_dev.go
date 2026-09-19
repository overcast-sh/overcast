//go:build dev

package s3

import "github.com/overcast-sh/overcast/internal/capabilities"

func init() {
	capabilities.Default.Register(
		// Buckets
		capabilities.Capability{Service: "s3", Operation: "CreateBucket", Category: "Buckets",
			Status: capabilities.StatusSupported, Notes: "Account regional namespaces via x-amz-bucket-namespace: account-regional"},
		capabilities.Capability{Service: "s3", Operation: "DeleteBucket", Category: "Buckets",
			Status: capabilities.StatusSupported, Notes: "Bucket must be empty"},
		capabilities.Capability{Service: "s3", Operation: "HeadBucket", Category: "Buckets",
			Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "s3", Operation: "ListBuckets", Category: "Buckets",
			Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "s3", Operation: "GetBucketLocation", Category: "Buckets",
			Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "s3", Operation: "GetBucketEncryption", Category: "Buckets",
			Status: capabilities.StatusSupported, Notes: "Returns default SSE-S3 config; stores AES256/KMS bucket encryption rules"},
		capabilities.Capability{Service: "s3", Operation: "PutBucketEncryption", Category: "Buckets",
			Status: capabilities.StatusSupported, Notes: "Stores AES256/KMS bucket encryption rules"},
		capabilities.Capability{Service: "s3", Operation: "DeleteBucketEncryption", Category: "Buckets",
			Status: capabilities.StatusSupported},

		// CORS
		capabilities.Capability{Service: "s3", Operation: "GetBucketCors", Category: "CORS",
			Status: capabilities.StatusPartial, Notes: "CORS rules; rule Id is not yet preserved"},
		capabilities.Capability{Service: "s3", Operation: "PutBucketCors", Category: "CORS",
			Status: capabilities.StatusPartial, Notes: "CORS rules; rule Id is not yet preserved"},
		capabilities.Capability{Service: "s3", Operation: "DeleteBucketCors", Category: "CORS",
			Status: capabilities.StatusSupported},

		// Website
		capabilities.Capability{Service: "s3", Operation: "GetBucketWebsite", Category: "Website",
			Status: capabilities.StatusPartial, Notes: "Returns the whole configuration — IndexDocument, ErrorDocument, RedirectAllRequestsTo and RoutingRules; Overcast serves no website endpoint, so nothing is actually redirected"},
		capabilities.Capability{Service: "s3", Operation: "PutBucketWebsite", Category: "Website",
			Status: capabilities.StatusPartial, Notes: "Stores IndexDocument, ErrorDocument, RedirectAllRequestsTo and RoutingRules with AWS's mutual exclusion and Protocol enum enforced; HttpRedirectCode values are not validated, and Overcast serves no website endpoint"},
		capabilities.Capability{Service: "s3", Operation: "DeleteBucketWebsite", Category: "Website",
			Status: capabilities.StatusSupported},

		// Objects
		capabilities.Capability{Service: "s3", Operation: "PutObject", Category: "Objects",
			Status: capabilities.StatusSupported, Notes: "Stores body + x-amz-meta-* headers; If-None-Match: * and If-Match make the write conditional, failing 412 — or 404 when If-Match finds no current version"},
		capabilities.Capability{Service: "s3", Operation: "GetObject", Category: "Objects",
			Status: capabilities.StatusSupported, Notes: "Returns body, ETag, metadata headers; versionId selects a specific version, and a delete-marked key answers 404 with x-amz-delete-marker"},
		capabilities.Capability{Service: "s3", Operation: "HeadObject", Category: "Objects",
			Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "s3", Operation: "DeleteObject", Category: "Objects",
			Status: capabilities.StatusSupported, Notes: "Idempotent — 204 for missing keys; in a versioned bucket a delete adds a delete marker, and versionId removes one version permanently"},
		capabilities.Capability{Service: "s3", Operation: "CopyObject", Category: "Objects",
			Status: capabilities.StatusSupported, Notes: "x-amz-copy-source may name a source versionId"},
		capabilities.Capability{Service: "s3", Operation: "ListObjectsV2", Category: "Objects",
			Status: capabilities.StatusSupported, Notes: "prefix, delimiter, max-keys (validated, clamped to 1000), start-after, continuation-token, encoding-type=url and fetch-owner; x-amz-request-payer and x-amz-optional-object-attributes accepted but Requester Pays and RestoreStatus are not emulated"},
		capabilities.Capability{Service: "s3", Operation: "DeleteObjects", Category: "Objects",
			Status: capabilities.StatusSupported, Notes: "Batch delete up to 1000 keys; quiet mode supported; per-entry VersionId, DeleteMarker and DeleteMarkerVersionId reported"},
		capabilities.Capability{Service: "s3", Operation: "ListObjects", Category: "Objects",
			Status: capabilities.StatusSupported, Notes: "Marker-based pagination; supports prefix, delimiter, encoding-type=url and the same max-keys validation as ListObjectsV2"},
		capabilities.Capability{Service: "s3", Operation: "GetObjectAttributes", Category: "Objects",
			Status: capabilities.StatusUnsupported, Notes: "stub; returns 501"},
		capabilities.Capability{Service: "s3", Operation: "PutObjectTagging", Category: "Objects",
			Status: capabilities.StatusSupported, Notes: "Up to 10 tags, unique keys, key 1-128 and value 0-256 characters; the tag character set is not validated"},
		capabilities.Capability{Service: "s3", Operation: "GetObjectTagging", Category: "Objects",
			Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "s3", Operation: "DeleteObjectTagging", Category: "Objects",
			Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "s3", Operation: "RestoreObject", Category: "Objects",
			Status: capabilities.StatusUnsupported, Notes: "Glacier restore simulation"},
		capabilities.Capability{Service: "s3", Operation: "SelectObjectContent", Category: "Objects",
			Status: capabilities.StatusUnsupported, Notes: "S3 Select (SQL queries on objects)"},

		// Multipart uploads
		capabilities.Capability{Service: "s3", Operation: "CreateMultipartUpload", Category: "Multipart uploads",
			Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "s3", Operation: "UploadPart", Category: "Multipart uploads",
			Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "s3", Operation: "UploadPartCopy", Category: "Multipart uploads",
			Status: capabilities.StatusUnsupported, Notes: "stub; returns 501"},
		capabilities.Capability{Service: "s3", Operation: "CompleteMultipartUpload", Category: "Multipart uploads",
			Status: capabilities.StatusSupported, Notes: "Validates the parts list as AWS does: a wrong or unknown ETag is InvalidPart, out-of-order parts InvalidPartOrder, a non-final part under 5 MiB EntityTooSmall, an empty list MalformedXML; a refused completion leaves the upload intact. Honours the same If-None-Match: * and If-Match conditional writes as PutObject, evaluated at completion time"},
		capabilities.Capability{Service: "s3", Operation: "AbortMultipartUpload", Category: "Multipart uploads",
			Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "s3", Operation: "ListMultipartUploads", Category: "Multipart uploads",
			Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "s3", Operation: "ListParts", Category: "Multipart uploads",
			Status: capabilities.StatusSupported},

		// ACLs & policies
		capabilities.Capability{Service: "s3", Operation: "GetBucketAcl", Category: "ACLs & policies",
			Status: capabilities.StatusUnsupported, Notes: "stub; returns 501"},
		capabilities.Capability{Service: "s3", Operation: "PutBucketAcl", Category: "ACLs & policies",
			Status: capabilities.StatusUnsupported, Notes: "stub; returns 501"},
		capabilities.Capability{Service: "s3", Operation: "GetObjectAcl", Category: "ACLs & policies",
			Status: capabilities.StatusUnsupported, Notes: "stub; returns 501"},
		capabilities.Capability{Service: "s3", Operation: "PutObjectAcl", Category: "ACLs & policies",
			Status: capabilities.StatusUnsupported, Notes: "stub; returns 501"},
		capabilities.Capability{Service: "s3", Operation: "GetBucketPolicy", Category: "ACLs & policies",
			Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "s3", Operation: "PutBucketPolicy", Category: "ACLs & policies",
			Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "s3", Operation: "DeleteBucketPolicy", Category: "ACLs & policies",
			Status: capabilities.StatusSupported},

		// Versioning
		capabilities.Capability{Service: "s3", Operation: "GetBucketVersioning", Category: "Versioning",
			Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "s3", Operation: "PutBucketVersioning", Category: "Versioning",
			Status: capabilities.StatusSupported, Notes: "Enabled and Suspended, with AWS's semantics for both; objects that predate the change become their key's null version"},
		capabilities.Capability{Service: "s3", Operation: "ListObjectVersions", Category: "Versioning",
			Status: capabilities.StatusSupported, Notes: "Versions and delete markers in AWS's order (key ascending, then most recent first), with prefix, delimiter, max-keys and key-marker/version-id-marker pagination"},

		// Tagging
		capabilities.Capability{Service: "s3", Operation: "GetBucketTagging", Category: "Tagging",
			Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "s3", Operation: "PutBucketTagging", Category: "Tagging",
			Status: capabilities.StatusSupported, Notes: "Up to 50 tags, unique keys, key 1-128 and value 0-256 characters; the tag character set is not validated"},
		capabilities.Capability{Service: "s3", Operation: "DeleteBucketTagging", Category: "Tagging",
			Status: capabilities.StatusSupported},

		// Lifecycle
		capabilities.Capability{Service: "s3", Operation: "GetBucketLifecycleConfiguration", Category: "Lifecycle",
			Status: capabilities.StatusSupported, Notes: "NoSuchLifecycleConfiguration when none is set; reports x-amz-transition-default-minimum-object-size"},
		capabilities.Capability{Service: "s3", Operation: "PutBucketLifecycleConfiguration", Category: "Lifecycle",
			Status: capabilities.StatusPartial, Notes: "Expiration, Transition, NoncurrentVersionExpiration, NoncurrentVersionTransition, ExpiredObjectDeleteMarker, AbortIncompleteMultipartUpload and prefix/tag/size filters are applied by an hourly sweeper; x-amz-transition-default-minimum-object-size gates transitions of objects under 128 KB, noncurrent ones included; expiring the current version of a versioned object adds a delete marker rather than deleting it"},
		capabilities.Capability{Service: "s3", Operation: "DeleteBucketLifecycle", Category: "Lifecycle",
			Status: capabilities.StatusSupported},

		// Notifications
		capabilities.Capability{Service: "s3", Operation: "GetBucketNotificationConfiguration", Category: "Notifications",
			Status: capabilities.StatusSupported, Notes: "Returns empty config if none set"},
		capabilities.Capability{Service: "s3", Operation: "PutBucketNotificationConfiguration", Category: "Notifications",
			Status: capabilities.StatusSupported, Notes: "SQS, SNS, Lambda and EventBridge destinations; prefix/suffix filters. Records carry versionId and sequencer; SNS deliveries carry the Records JSON as the notification envelope's Message string with Subject \"Amazon S3 Notification\", as real S3 does; EventBridge events carry AWS's Object Created/Object Deleted shape, including deletion-type, minus the fields Overcast has no value for"},
	)
}
