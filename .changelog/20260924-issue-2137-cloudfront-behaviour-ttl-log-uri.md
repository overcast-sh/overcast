* [cloudfront] a viewer-request function that rewrites the `uri` no longer changes how long the response is cached.
  the TTL comes from the cache behaviour the viewer's path matched, as on AWS, not from the behaviour the new `uri` would match.
* [cloudfront] a viewer-request function that returns a `uri` not beginning with `/` gets a 502, and the origin is not called.
  AWS treats this as a function validation error.
* [cloudfront] the access log's `cs-uri-stem` is the viewer's path, not Overcast's internal `/_overcast/cloudfront/distributions/{id}/...` route.
  free-text fields use CloudFront's log encoding, so an encoded `%20` in the path is logged as `%2520`.
