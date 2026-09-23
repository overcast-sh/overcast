package s3tables

// Table bucket and table ARNs: the one place they are built and taken apart.

import (
	"regexp"

	"github.com/overcast-sh/overcast/internal/protocol"
)

// bucketARN is arn:aws:s3tables:<region>:<account>:bucket/<name>.
func (s *Service) bucketARN(region, name string) string {
	return "arn:aws:s3tables:" + region + ":" + s.accountID() + ":bucket/" + name
}

// tableARN names a table by its id under its bucket's ARN, so it survives
// RenameTable.
func tableARN(bucketARN, tableID string) string {
	return bucketARN + "/table/" + tableID
}

// bucketARNPattern is the model's TableBucketARN pattern with the service
// component fixed to s3tables; tableARNPattern is TableARN's.
var (
	bucketARNPattern = regexp.MustCompile(`^arn:(aws[-a-z0-9]*):s3tables:([-a-z0-9]*):([0-9]{12}):bucket/([a-z0-9_-]{3,63})$`)
	tableARNPattern  = regexp.MustCompile(`^arn:(aws[-a-z0-9]*):s3tables:([-a-z0-9]*):([0-9]{12}):bucket/([a-z0-9_-]{3,63})/table/([a-zA-Z0-9_-]{1,255})$`)
)

// parsedARN is a table bucket or table ARN taken apart.
type parsedARN struct {
	Region  string
	Account string
	Bucket  string
	TableID string // empty for a bucket ARN
}

func parseBucketARN(arn string) (parsedARN, *protocol.AWSError) {
	m := bucketARNPattern.FindStringSubmatch(arn)
	if m == nil {
		return parsedARN{}, badRequest("The specified table bucket ARN is not valid.")
	}
	return parsedARN{Region: m[2], Account: m[3], Bucket: m[4]}, nil
}

func parseTableARN(arn string) (parsedARN, *protocol.AWSError) {
	m := tableARNPattern.FindStringSubmatch(arn)
	if m == nil {
		return parsedARN{}, badRequest("The specified table ARN is not valid.")
	}
	return parsedARN{Region: m[2], Account: m[3], Bucket: m[4], TableID: m[5]}, nil
}

// parseResourceARN accepts either kind, which is what the tagging operations'
// ResourceArn names.
func parseResourceARN(arn string) (parsedARN, *protocol.AWSError) {
	if p, aerr := parseTableARN(arn); aerr == nil {
		return p, nil
	}
	if p, aerr := parseBucketARN(arn); aerr == nil {
		return p, nil
	}
	return parsedARN{}, badRequest("The specified resource ARN is not valid.")
}
