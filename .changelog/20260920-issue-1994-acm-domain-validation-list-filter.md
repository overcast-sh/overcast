* [acm] domain names are validated, `DescribeCertificate` returns `DomainValidationOptions`, and `ListCertificates` honours `CertificateStatuses` (#1994).
  a `DomainName` or SAN outside AWS's domain pattern, or an over-long one, is refused with a `ValidationException` naming the member — any non-empty string used to be issued a certificate
  `DescribeCertificate` reports one entry per domain — `SUCCESS`, the requested method, and for `DNS` a CNAME that stays the same across calls, so publish-then-wait flows have something to act on
  the status filter used to be ignored, so a `PENDING_VALIDATION` poll saw its own issued certificate; an unknown status is now refused rather than matching everything
