* [apigateway] host-style invoke URLs keep `%2F` inside a path segment, so `/@scope%2fpkg` matches `/{package}` as on AWS.
  it used to be decoded into a separator and answer 403 `Missing Authentication Token`; path-style invoke was unaffected.
* [elbv2/cloudfront/lambda] load balancers, CloudFront and function URLs pass a `%2F` in the path on still encoded.
  ALB targets and CloudFront origins receive the path as the client sent it; a function URL's `rawPath` keeps its encoding.
