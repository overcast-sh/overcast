+ [cloudwatch-logs/cloudwatch/cloudformation] CloudWatch Logs metric filters, and `AWS::Logs::MetricFilter` to provision them.
  `PutMetricFilter`, `DescribeMetricFilters`, `DeleteMetricFilter` and `TestMetricFilter`, over AWS JSON and RPC v2 CBOR
  a log event that matches a filter publishes a CloudWatch datapoint, so `GetMetricStatistics` reports it and an alarm on the metric fires from log lines alone
  nothing is published while `OVERCAST_SERVICE_METRICS=disabled`; the filters are still stored, described and tested
