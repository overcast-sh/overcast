* [lambda-web] endpoint and account rate limits now apply before authentication, and package builds are bounded (#2302).
  unsigned requests now spend an IamAuth endpoint's tokens, so a flood can starve signed callers of a low `rateLimit`; the endpoint's limit is checked before the account's.
  at most two packages are prepared at once; a package over 100,000 entries, or past 512 MB counting 512 bytes of header per entry, makes its revision `Failed`.
