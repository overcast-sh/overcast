~ [athena] the query engine image is `ghcr.io/overcast-sh/overcast-athena-engine`, Trino 483 with only Hive and Iceberg: a 0.78 GB first pull.
  it takes 1.86 GB on disk, against 2.43 GB for the stock `trinodb/trino` image.
  a stock `trinodb/trino` set through `ATHENA_ENGINE_IMAGE` loads all of its connectors.
