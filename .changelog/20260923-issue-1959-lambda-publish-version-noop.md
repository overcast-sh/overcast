*! [lambda] `PublishVersion` returns the existing version instead of allocating a new one when nothing changed since it
  matches AWS: code and configuration are compared against the highest existing version; `RevisionId`/`CodeSha256` mismatches answer `PreconditionFailedException`/`InvalidParameterValueException`
  `PublishTo` returns 501 before mutation, matching `CreateFunction`'s gate
  migration: a script or test that publishes twice with nothing changed in between gets the same version back, as on AWS; change the code or configuration between publishes to get a new one
