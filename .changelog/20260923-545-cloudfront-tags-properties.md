* [cloudformation/cloudfront] AWS::CloudFront::Distribution now provisions with tags.
  Create dispatches to CreateDistributionWithTags when Tags is set, so
  ListTagsForResource and the _custom_id_ tag both work again.
