+ [rds] `CreateDBInstance`/`CreateDBCluster` accept `ManageMasterUserPassword`, RDS's own generated-and-managed master password.
  the generated password lands in a new Secrets Manager secret (`rds!db-<uuid>`/`rds!cluster-<uuid>`), returned as `MasterUserSecret`.
  `ModifyDBInstance`/`ModifyDBCluster` turn it on or off; turning it off, or deleting the DB, deletes the secret, as on AWS.
* [cloudformation] `AWS::RDS::DBInstance`/`AWS::RDS::DBCluster` forward `ManageMasterUserPassword`, `MasterUserSecret.KmsKeyId` and `Tags`.
  those were previously dropped entirely; `Fn::GetAtt MasterUserSecret.SecretArn` now resolves and `Tags` reconcile on update.
