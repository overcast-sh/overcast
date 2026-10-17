* [ecr] `DescribeRepositories`, `ListImages`, `DescribeImages` page with `maxResults`/`nextToken`; `Delete*Policy` return the deleted text (#2245)
  default page is 100 as on AWS; a bad `maxResults` or `nextToken` is `InvalidParameterException`; the web console now follows `nextToken`.
