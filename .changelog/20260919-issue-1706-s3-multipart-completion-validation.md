* [s3] `CompleteMultipartUpload` checks part ETags, ordering and the 5 MiB minimum, answering `InvalidPart`, `InvalidPartOrder` or `EntityTooSmall` (#1706)
  an empty parts list is `MalformedXML`, and a refused completion leaves the upload intact to retry or abort
