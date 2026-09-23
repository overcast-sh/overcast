* [lambda] `GetFunction` and `GetFunctionConfiguration` honour the `Qualifier` parameter
  resolves a version number, an alias (through its `FunctionVersion`), or `$LATEST` by default, embedded in the name or given as a query parameter
  an unknown version or alias answers `ResourceNotFoundException` instead of silently reporting `$LATEST`
