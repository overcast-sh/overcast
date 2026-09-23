+ [compat] every release ships `compat-report.json`, the public compatibility report
  Each result that does not pass carries a reason code and, where one exists, its tracking issue. `go run ./cmd/compat --publish-report` builds it; overcast.sh/compat renders it.
