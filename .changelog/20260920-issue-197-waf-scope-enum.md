* [waf] `Scope` is validated as AWS's `CLOUDFRONT | REGIONAL` enum, where any string used to be accepted and create an unreachable namespace (#197).
  CreateWebACL, GetWebACL, ListWebACLs and DeleteWebACL answer `WAFInvalidParameterException` (400) for a value outside the enum, on both the JSON and RPC v2 CBOR paths
