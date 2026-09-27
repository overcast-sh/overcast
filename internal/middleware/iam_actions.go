package middleware

import "strings"

// iamActionRule is the IAM action AWS checks for one API operation, where it
// is not "<prefix>:<Operation>".
type iamActionRule struct {
	// action is the action checked.
	action string
	// versioned, when set, is the action checked instead when the request
	// names an object version (S3's versionId).
	versioned string
}

// operationIAMActions holds, per Overcast service key, the operations AWS
// authorises by an action not named after them. It is the one table of them:
// every other operation is authorised as "<prefix>:<Operation>" (iamAction).
//
// S3's rows follow AWS's "Required permissions for Amazon S3 API operations"
// (https://docs.aws.amazon.com/AmazonS3/latest/userguide/using-with-s3-policy-actions.html).
// A policy is written from that page, so an operation authorised by its own
// name instead — s3:ListObjectsV2, s3:HeadObject, s3:CopyObject — matches no
// statement a user wrote and denies a call AWS would serve.
var operationIAMActions = map[string]map[string]iamActionRule{
	// AWS authorises all three invoke operations with lambda:InvokeFunction;
	// there is no lambda:Invoke.
	"lambda": {
		"Invoke":                   {action: "lambda:InvokeFunction"},
		"InvokeAsync":              {action: "lambda:InvokeFunction"},
		"InvokeWithResponseStream": {action: "lambda:InvokeFunction"},
	},
	"s3": {
		// The service.
		"ListBuckets":          {action: "s3:ListAllMyBuckets"},
		"ListDirectoryBuckets": {action: "s3express:ListAllMyDirectoryBuckets"},
		"CreateSession":        {action: "s3express:CreateSession"},

		// Listing a bucket.
		"HeadBucket":           {action: "s3:ListBucket"},
		"ListObjects":          {action: "s3:ListBucket"},
		"ListObjectsV2":        {action: "s3:ListBucket"},
		"ListObjectVersions":   {action: "s3:ListBucketVersions"},
		"ListMultipartUploads": {action: "s3:ListBucketMultipartUploads"},

		// Bucket configuration, whose Delete and List operations are
		// authorised by the Put and Get actions.
		"GetBucketAccelerateConfiguration":            {action: "s3:GetAccelerateConfiguration"},
		"PutBucketAccelerateConfiguration":            {action: "s3:PutAccelerateConfiguration"},
		"GetBucketAnalyticsConfiguration":             {action: "s3:GetAnalyticsConfiguration"},
		"ListBucketAnalyticsConfigurations":           {action: "s3:GetAnalyticsConfiguration"},
		"PutBucketAnalyticsConfiguration":             {action: "s3:PutAnalyticsConfiguration"},
		"DeleteBucketAnalyticsConfiguration":          {action: "s3:PutAnalyticsConfiguration"},
		"GetBucketCors":                               {action: "s3:GetBucketCORS"},
		"PutBucketCors":                               {action: "s3:PutBucketCORS"},
		"DeleteBucketCors":                            {action: "s3:PutBucketCORS"},
		"GetBucketEncryption":                         {action: "s3:GetEncryptionConfiguration"},
		"PutBucketEncryption":                         {action: "s3:PutEncryptionConfiguration"},
		"DeleteBucketEncryption":                      {action: "s3:PutEncryptionConfiguration"},
		"GetBucketIntelligentTieringConfiguration":    {action: "s3:GetIntelligentTieringConfiguration"},
		"ListBucketIntelligentTieringConfigurations":  {action: "s3:GetIntelligentTieringConfiguration"},
		"PutBucketIntelligentTieringConfiguration":    {action: "s3:PutIntelligentTieringConfiguration"},
		"DeleteBucketIntelligentTieringConfiguration": {action: "s3:PutIntelligentTieringConfiguration"},
		"GetBucketInventoryConfiguration":             {action: "s3:GetInventoryConfiguration"},
		"ListBucketInventoryConfigurations":           {action: "s3:GetInventoryConfiguration"},
		"PutBucketInventoryConfiguration":             {action: "s3:PutInventoryConfiguration"},
		"DeleteBucketInventoryConfiguration":          {action: "s3:PutInventoryConfiguration"},
		"GetBucketLifecycleConfiguration":             {action: "s3:GetLifecycleConfiguration"},
		"PutBucketLifecycleConfiguration":             {action: "s3:PutLifecycleConfiguration"},
		"DeleteBucketLifecycle":                       {action: "s3:PutLifecycleConfiguration"},
		"GetBucketMetadataConfiguration":              {action: "s3:GetBucketMetadataTableConfiguration"},
		"CreateBucketMetadataConfiguration":           {action: "s3:CreateBucketMetadataTableConfiguration"},
		"DeleteBucketMetadataConfiguration":           {action: "s3:DeleteBucketMetadataTableConfiguration"},
		"GetBucketMetricsConfiguration":               {action: "s3:GetMetricsConfiguration"},
		"ListBucketMetricsConfigurations":             {action: "s3:GetMetricsConfiguration"},
		"PutBucketMetricsConfiguration":               {action: "s3:PutMetricsConfiguration"},
		"DeleteBucketMetricsConfiguration":            {action: "s3:PutMetricsConfiguration"},
		"GetBucketNotificationConfiguration":          {action: "s3:GetBucketNotification"},
		"PutBucketNotificationConfiguration":          {action: "s3:PutBucketNotification"},
		"DeleteBucketOwnershipControls":               {action: "s3:PutBucketOwnershipControls"},
		"GetBucketReplication":                        {action: "s3:GetReplicationConfiguration"},
		"PutBucketReplication":                        {action: "s3:PutReplicationConfiguration"},
		"DeleteBucketReplication":                     {action: "s3:PutReplicationConfiguration"},
		"DeleteBucketTagging":                         {action: "s3:PutBucketTagging"},
		"GetObjectLockConfiguration":                  {action: "s3:GetBucketObjectLockConfiguration"},
		"PutObjectLockConfiguration":                  {action: "s3:PutBucketObjectLockConfiguration"},
		"GetPublicAccessBlock":                        {action: "s3:GetBucketPublicAccessBlock"},
		"PutPublicAccessBlock":                        {action: "s3:PutBucketPublicAccessBlock"},
		"DeletePublicAccessBlock":                     {action: "s3:PutBucketPublicAccessBlock"},

		// Reading an object.
		"GetObject":           {action: "s3:GetObject", versioned: "s3:GetObjectVersion"},
		"GetObjectAttributes": {action: "s3:GetObject", versioned: "s3:GetObjectVersion"},
		// HeadObject's row in AWS's table names only s3:GetObject, but its API
		// reference asks for "the relevant read object (or version)
		// permission", as GetObject does.
		"HeadObject":          {action: "s3:GetObject", versioned: "s3:GetObjectVersion"},
		"GetObjectTorrent":    {action: "s3:GetObject"},
		"SelectObjectContent": {action: "s3:GetObject"},
		"GetObjectAcl":        {action: "s3:GetObjectAcl", versioned: "s3:GetObjectVersionAcl"},
		"GetObjectTagging":    {action: "s3:GetObjectTagging", versioned: "s3:GetObjectVersionTagging"},

		// Writing an object. A copy also reads its source (s3IAMChecks).
		"CopyObject":              {action: "s3:PutObject"},
		"CreateMultipartUpload":   {action: "s3:PutObject"},
		"UploadPart":              {action: "s3:PutObject"},
		"UploadPartCopy":          {action: "s3:PutObject"},
		"CompleteMultipartUpload": {action: "s3:PutObject"},
		"ListParts":               {action: "s3:ListMultipartUploadParts"},
		"PutObjectAcl":            {action: "s3:PutObjectAcl", versioned: "s3:PutObjectVersionAcl"},
		"PutObjectTagging":        {action: "s3:PutObjectTagging", versioned: "s3:PutObjectVersionTagging"},

		// Deleting an object. DeleteObjects is checked once per key.
		"DeleteObject":        {action: "s3:DeleteObject", versioned: "s3:DeleteObjectVersion"},
		"DeleteObjects":       {action: "s3:DeleteObject", versioned: "s3:DeleteObjectVersion"},
		"DeleteObjectTagging": {action: "s3:DeleteObjectTagging", versioned: "s3:DeleteObjectVersionTagging"},

		"WriteGetObjectResponse": {action: "s3-object-lambda:WriteGetObjectResponse"},
	},
}

// iamAction is op's IAM action for svc, or "" when there is no operation or
// svc is not a service IAM authorises.
func iamAction(svc, op string) string {
	return iamActionFor(svc, op, false)
}

// iamActionFor is iamAction for a request that may name an object version:
// versioned selects the action AWS checks for a versionId.
func iamActionFor(svc, op string, versioned bool) string {
	op = strings.TrimSpace(op)
	if op == "" || svc == "" || svc == "internal" || svc == "metrics" || svc == "events" {
		return ""
	}
	rule, ok := operationIAMActions[svc][op]
	switch {
	case !ok:
		return iamActionPrefix(svc) + ":" + op
	case versioned && rule.versioned != "":
		return rule.versioned
	}
	return rule.action
}
