* [s3/router] S3 buckets named `tags` or `applications` work through the SDK and unsigned, instead of being read as another bucket (#2098).
  an S3-signed request to `/applications` now reaches S3 rather than AppRegistry, and every shared root's S3 fallback sees the whole request path.
