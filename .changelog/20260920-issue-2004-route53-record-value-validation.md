* [route53] `ChangeResourceRecordSets` rejects a record value that does not fit its record type
  An `A` whose value is not a dotted quad, an `AAAA` holding an IPv4 address or a `CNAME` with two values answers `InvalidChangeBatch`, as on AWS, instead of being stored.
  `NS`, `PTR`, `MX`, `SRV`, `CAA`, `TXT` and `SPF` are checked too; `NAPTR`, `DS`, `TLSA`, `SSHFP`, `SVCB` and `HTTPS` values are still stored as given.
