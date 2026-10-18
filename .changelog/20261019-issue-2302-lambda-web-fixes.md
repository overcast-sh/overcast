* [lambda-web] a resource-policy or tag change now takes effect on the next request, and exited containers are removed instead of piling up (#2302).
  a restart settles endpoints left Pending, Deleting or mid-update, and `POST /_overcast/reset` retires the endpoints it wiped; `OVERCAST_STATE_LAMBDAWEB` is honoured.
  list calls resume after the last item of the previous page, so a new revision no longer repeats or skips one; a map node counts only its own Region's environments.
* [lambda-web] endpoints answer their own CORS preflights and set their own CORS headers, as on AWS (#2302).
  the emulator's blanket CORS no longer wraps host-routed endpoints, and an application's own `Access-Control-*` headers replace the invoke path's.
*! [lambda-web] only `nodejs24.x` is accepted as a runtime, and a revision's environment variables are limited to 32 KB (#2302).
  migration: use `nodejs24.x`; keep the names and values of `environmentVariables` under 32 KB in aggregate.
*! [lambda-web] resource policies follow AWS's public-policy rules for wildcard principals and condition operators (#2302).
  refused: a wildcard inside a principal ARN, a negated or IfExists operator, a wildcard account in `aws:SourceArn`. Accepted now: `distribution/*` under a fixed account.
  migration: name principals in full, or pin a trusted condition key to a fixed account with a positive operator.
* [lambda-web] environments get the documented 512 MB `/tmp`, 1,024 open files and 1,024 processes, and `Upgrade` is removed from requests (#2302).
