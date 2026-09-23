+ [web/stepfunctions] a Lambda Task's state inspector shows each attempt's invocation: its logs, REPORT metrics, log stream and error
  Opens by default on a Lambda Task that failed, was caught or retried; matched by request id, the attempt's error, or timing, and says which.
  "Replay in Lambda" saves the attempt's event as a test event and opens the function's Test tab.
~ [web/stepfunctions] Step Functions errors show the function's own error type, message and stack trace, with a hint for common Lambda failures
+ [web] state machine, execution, IAM and log-stream ARNs link to their console pages, in the region the ARN names
* [lambda] a synchronous Invoke now answers with the invocation's request id in x-amzn-RequestId
  It was absent, so SDK callers read an empty id and Step Functions' lambda:invoke returned SdkResponseMetadata.RequestId "".
* [lambda] a timed-out invocation's REPORT Duration is the timeout, no longer the timeout plus up to 2 s of output wait
* [web] accepting the connection dialog's prefilled endpoint opens the console without a reload
* [web] a `?region=` in the URL is no longer overwritten at startup by the server's default region
