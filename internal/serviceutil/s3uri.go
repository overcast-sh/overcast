package serviceutil

import "strings"

// SplitS3URI takes "s3://bucket/key" apart into its bucket and key, which is
// empty for "s3://bucket" and "s3://bucket/". ok is false for anything that
// is not an s3:// URI with a bucket.
func SplitS3URI(uri string) (bucket, key string, ok bool) {
	rest, found := strings.CutPrefix(uri, "s3://")
	if !found {
		return "", "", false
	}
	bucket, key, _ = strings.Cut(rest, "/")
	return bucket, key, bucket != ""
}
