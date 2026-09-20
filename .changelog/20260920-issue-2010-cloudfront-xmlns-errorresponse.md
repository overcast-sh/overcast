* [cloudfront] Responses now carry the 2020-05-31 XML namespace and errors use the restXml `ErrorResponse` envelope.
    Every response root declares `xmlns="http://cloudfront.amazonaws.com/doc/2020-05-31/"`, the namespace CloudFront's API has always specified; nested elements inherit it.
    Errors move from S3's bare `<Error>` body to `<ErrorResponse><Error><Type>…</Type><Code>…</Code><Message>…</Message></Error><RequestId>…</RequestId></ErrorResponse>`; codes and statuses are unchanged.
    `Distribution`, `DistributionConfig` and `DistributionSummary` emit their members in the order AWS documents rather than the order they were declared in.
