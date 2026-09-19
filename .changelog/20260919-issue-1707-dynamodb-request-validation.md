* [dynamodb] nine request-validation rules AWS documents are now enforced with AWS's wording, where each request used to succeed with `200` (#1707).
  items over 400 KB by AWS's size accounting, a `Query` that does not constrain the partition key, and empty-string key attributes are refused with a `ValidationException`
  `BatchWriteItem` and `TransactWriteItems` refuse two operations on one item, `BatchGetItem` refuses more than 100 keys, and `Scan` refuses a `Segment` at or past `TotalSegments`
  `CreateTable` refuses `AttributeDefinitions` that do not match the table and index key schemas exactly, choosing between AWS's four wordings as AWS does
