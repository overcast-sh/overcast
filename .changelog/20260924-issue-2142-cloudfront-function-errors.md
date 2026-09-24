* [cloudfront] a CloudFront Function that throws, doesn't compile or has no `handler` now answers 503, as on AWS.
  it used to answer 500 when it threw. When it failed to compile it was skipped, and the request went to the origin as if no function were attached.
* [cloudfront] a CloudFront Function that returns something other than a request or response object now answers 502, as on AWS.
  this covers forgetting to `return`. The function used to be skipped silently.
* [cloudfront] viewer-response functions no longer run when the origin answers 400 or above, as AWS documents.
