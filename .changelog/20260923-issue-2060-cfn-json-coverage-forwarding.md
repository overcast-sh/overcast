* [cloudformation] forward CloudTrail, AppConfig, OpenSearch, ACM, Athena, Shield and Transfer properties the backing service already supports
  a tags-only update to an AppConfig, OpenSearch, ACM, Athena or Shield resource, including a stack-tag change, now reconciles the tags in place instead of replacing the resource, as on AWS
*! [cloudformation/ses] `AWS::SES::ConfigurationSet` fails the resource with SES's own 501 instead of reporting a success that created nothing
  SES configuration sets are not implemented, so the stub's CREATE_COMPLETE left templates relying on a configuration set that did not exist
  migration: keep `AWS::SES::ConfigurationSet` out of the template you deploy to Overcast, for example behind a condition, until SES implements configuration sets
