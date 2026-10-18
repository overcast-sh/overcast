* [cloudfront] a distribution can front a Lambda Web Function endpoint through an origin access control of type `lambda` (#2302).
  signing behavior `always-amz-auth` SigV4-signs origin requests for service `lambda` and passes the viewer `Authorization` header through.
*! [cloudfront] origin access control creation and update reject an unknown origin type, signing protocol or behavior with `InvalidArgument` (#2302).
  `always-amz-auth` on a non-`lambda` OAC is `InvalidArgument`; a `lambda` OAC on a non-Web-Function origin is `InvalidDomainNameForOriginAccessControl`.
  migration: use origin type `s3`, `mediastore`, `mediapackagev2` or `lambda`, protocol `sigv4` or `sigv4a`, behavior `never`, `always`, `no-override` or `always-amz-auth` (lambda only).
