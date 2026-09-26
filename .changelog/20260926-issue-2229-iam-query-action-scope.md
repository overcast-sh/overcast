security [iam/router] IAM enforcement authorises an AWS Query call as the operation the router serves, not as its credential scope (#2229).
  an IAM `CreateUser` signed for `s3` was checked as `s3:CreateUser`, so a principal allowed only `s3:*` could create IAM users.
  applies to every Query service on `POST /` and `GET /?Action=`, including a call with no `Version` or a large leading parameter.
