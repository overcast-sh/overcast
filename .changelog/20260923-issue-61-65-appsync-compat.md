*! [appsync] `CreateGraphqlApi` no longer auto-creates a default API key for `authenticationType=API_KEY`
  AWS's `CreateGraphqlApi` response carries no `apiKey` field; the console's "default key" is a separate `CreateApiKey` call it makes, not something the API itself does
  migration: call `CreateApiKey` explicitly after `CreateGraphqlApi` if you relied on the implicit key
*! [appsync] `CreateGraphqlApi`/`UpdateGraphqlApi` validate `logConfig` and `additionalAuthenticationProviders` shape
  fieldLogLevel and each provider's authenticationType are checked against AWS's documented enums; these passthrough fields previously accepted any shape
  migration: fix a `logConfig` missing `fieldLogLevel`, or a provider naming an unrecognized `authenticationType`, before deploying
+ [appsync] `CreateGraphqlApi` boundary tests cover `queryDepthLimit` (0/75/76) and `resolverCountLimit` (0/10000/10001)
