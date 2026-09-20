* [shield] Shield protections now refuse a duplicate resource, carry their own ARN, and ListProtections filters and paginates.
  A second `CreateProtection` for a `ResourceArn` that already has a protection reports `ResourceAlreadyExistsException` instead of creating a second record.
  `Protection` carries `ProtectionArn` (`arn:aws:shield::<account>:protection/<id>`) on `DescribeProtection` and `ListProtections`, as real Shield always does.
  `ListProtections` honours `InclusionFilters`, `MaxResults` and `NextToken`, rejecting an unrecognised token with `InvalidPaginationTokenException`.
