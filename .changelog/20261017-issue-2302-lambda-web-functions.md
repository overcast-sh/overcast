* [lambda-web] AWS Lambda Web Functions control plane: all 20 operations of the `lambda-web` model, with CloudFormation and system-map support (#2302).
  Web functions, immutable revisions, and `HomeRegion`, `MultiRegion` and `PerRegion` endpoints with revision weights and `LatestRevision` auto-deployment.
  Nothing runs yet: a revision's package is never fetched and an endpoint's domain name is allocated but not served.
  CloudFormation `AWS::Lambda::WebFunction`, `WebFunctionRevision` and `WebFunctionEndpoint`; the system map shows each function's endpoints.
