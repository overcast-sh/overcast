~ [cloudfront/docs] CloudFront's operation count and docs no longer present the emulator-only `ProxyRequest` as an AWS API operation.
  The service reads 88 AWS operations rather than 89 — in STATUS.md, the docs service index, `service-support.json` and the CloudFront pages.
  The operations table lists `ProxyRequest` under its own "Emulator extensions" heading, without the `API_ProxyRequest.html` link it used to carry: AWS has no such page.
  Capability rows carry an `EmulatorOnly` flag for this, and `capgen --check-model` rejects it on an operation AWS does model.
