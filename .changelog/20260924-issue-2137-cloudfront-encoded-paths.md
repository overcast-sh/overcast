* [cloudfront] a request path containing an encoded `%` (`/100%25`) reaches the origin instead of answering 502, for custom and emulated origins alike.
  the proxy now works on the path exactly as the viewer encoded it, so the origin always receives it byte-for-byte.
* [cloudfront] a viewer-request function's `event.request.uri` is percent-encoded as the viewer sent it, not decoded, as on AWS.
  a `uri` returned with raw UTF-8 or spaces reaches the origin percent-encoded.
* [cloudfront] cache behaviour path patterns match after decoding escaped unreserved characters, so `/%7Euser` matches `/~user/*`.
  other escapes stay encoded for matching, as in AWS's RFC 3986 normalisation: `%40` does not match `@`.
