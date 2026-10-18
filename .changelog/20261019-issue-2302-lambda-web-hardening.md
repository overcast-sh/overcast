* [lambda-web] endpoint and account rate limits now apply before authentication, and package builds are bounded (#2302).
  an endpoint's limit is checked before the account's, so refused requests no longer spend account tokens; unsigned floods meet the limit before signature verification.
  at most two packages are prepared at once, and a package is refused over 100,000 entries or when entry headers push it past 512 MB.
