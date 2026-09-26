* [cloudfront/route53] Overcast's own CloudFront and Route 53 errors come wrapped in `<ErrorResponse>`, so an SDK reads the code. S3's stay a bare `<Error>`.
  covers the 501 for an unimplemented operation, a credential-scope mismatch and a SigV4 rejection; the models' `noErrorWrapping` trait picks the envelope.
  SigV4 rejections and the storage-migration 503 follow the request's wire protocol.
  so SES v2 gets JSON, CloudWatch over RPC v2 gets CBOR, and EC2 gets `<Response><Errors>`.
