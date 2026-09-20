~ [apigateway/appsync/eks] API Gateway, AppSync and EKS stop counting their emulator-only execution helpers as AWS API operations.
  API Gateway reads 102 of 104 (was 104 of 106), AppSync reads 81 of 81 (was 82 of 82), EKS reads 49 of 49 (was 50 of 50).
  ExecuteRestAPI, ExecuteV2API, ExecuteGraphQL and UpdateKubeconfig carry the EmulatorOnly flag and list under an "Emulator extensions" heading (#2007's pattern).
