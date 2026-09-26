* [s3] A `PutObject` that fails part way no longer destroys the object already stored at that key.
  the body is staged and moved into place only once it has all arrived and its record is stored; unversioned, versioned and suspended buckets alike.
  `CopyObject`, `UploadPart` and `CompleteMultipartUpload` share the fix; staged leftovers from a crash are discarded on the next write.
* [s3] `CopyObject` of an object onto itself, the documented way to replace its metadata, keeps its bytes instead of emptying it.
* [s3] Overwriting or deleting an object on Windows while a `GetObject` streams it succeeds, and the reader still gets the bytes it started on.
* [s3] Concurrent `CreateBucket` calls for one name create one bucket and publish one creation event; every other caller gets the existing-bucket answer.
