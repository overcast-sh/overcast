+ [glue/athena/s3tables] S3 Tables in the Data Catalog as `s3tablescatalog`, one catalog per table bucket, as AWS's analytics integration mounts them
  Glue `GetCatalog` and `GetCatalogs`, and the database and table reads accept `<account>:s3tablescatalog/<bucket>` as `CatalogId`
  Athena's metadata operations read `s3tablescatalog/<bucket>`; Lake Formation is not modelled, so everything is readable
* [glue] an operation naming `s3tablescatalog` as `CatalogId` is a 501 rather than applied to the account's own catalog
* [athena] a query run in an `s3tablescatalog/<bucket>` catalog fails `NOT_SUPPORTED` rather than reading `AwsDataCatalog` in its place
