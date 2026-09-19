* [router/web] the topology graph draws a `pipe` edge for any EventBridge pipe whose `Source` and `Target` ARNs name SQS, SNS, DynamoDB stream or Lambda resources
  the builder previously assumed every pipe was DynamoDB → SQS, so a pipe such as SQS → Lambda (for example from `AWS::Pipes::Pipe`) produced no edge in `GET /_overcast/topology`
