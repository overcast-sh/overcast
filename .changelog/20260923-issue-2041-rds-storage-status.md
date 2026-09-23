* [rds] `CreateDBInstance` rejects `AdditionalStorageVolumes` instead of silently dropping it, and `DBInstance` models the resting `StorageOperationStatus` fields
  Oracle/SQL Server only on AWS and neither engine is emulated, so it is now `InvalidParameterCombination`.
  The two storage-operation fields stay correctly omitted since Overcast runs no storage operation.
