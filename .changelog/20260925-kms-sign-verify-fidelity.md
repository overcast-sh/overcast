*! [kms] `Verify` of a signature that does not verify fails with `KMSInvalidSignatureException`, as on AWS, not `SignatureValid: false` (#2160)
  migration: treat `KMSInvalidSignatureException` as the failed-verification result, the same code that runs against AWS.
* [kms] `Sign` and `Verify` honour `SigningAlgorithm` and `MessageType`: every RSASSA-PSS and PKCS #1 v1.5 algorithm, `RAW` or `DIGEST` (#2160)
  A digest whose length does not match the algorithm's hash, or a missing or unknown algorithm, is a `ValidationException`.
* [kms] `Sign`/`Verify` return `InvalidKeyUsageException` for a non-`SIGN_VERIFY` key or an algorithm its `KeySpec` lacks (#2160)
  They previously answered a retryable 500 `InternalError`, or signed with an encryption key. A disabled key is now a `DisabledException`.
