* [s3tables] `GetTable` answers the path binding older SDKs send, `GET /tables/{tableBucketARN}/{namespace}/{name}`, as well as `/get-table` (#2264).
  the Rust SDK before the June 2025 model and the .NET SDK 4.0.0 were refused with "Credential should be scoped to correct service: 'mediastore'"; IAM authorises the old binding as `s3tables:GetTable`.
* [router] a signed REST call that only MediaStore Data's catch-all `/{Path+}` binding matches now gets its own service's 501, not a MediaStore scope error.
