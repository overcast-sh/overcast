package serviceutil

import "testing"

func TestSplitS3URI(t *testing.T) {
	cases := []struct {
		uri, bucket, key string
		ok               bool
	}{
		{"s3://logs/athena/results/", "logs", "athena/results/", true},
		{"s3://logs/", "logs", "", true},
		{"s3://logs", "logs", "", true},
		{"s3:///key", "", "key", false},
		{"https://logs.s3.amazonaws.com/key", "", "", false},
		{"", "", "", false},
	}
	for _, c := range cases {
		t.Run(c.uri, func(t *testing.T) {
			// Given: a location; When: it is split; Then: bucket and key are the URI's parts
			bucket, key, ok := SplitS3URI(c.uri)
			if bucket != c.bucket || key != c.key || ok != c.ok {
				t.Errorf("SplitS3URI(%q) = %q, %q, %v; want %q, %q, %v", c.uri, bucket, key, ok, c.bucket, c.key, c.ok)
			}
		})
	}
}
