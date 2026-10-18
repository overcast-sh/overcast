package lambdaweb

// errTypeCodeUnavailable is an initFailure type internal to Overcast: the
// provider could not read the revision's package from S3 (deleted object,
// revoked access). It is never sent to a client; the engine turns it into
// Platform.RevisionNotActive and marks the revision Failed, as AWS does when it
// next verifies access to the object.
const errTypeCodeUnavailable = "Overcast.RevisionCodeUnavailable"

// logGroupFor is the log group a revision's logs go to: the loggingConfig
// group, else /aws/lambda/web/{function}.
func logGroupFor(function string, rev *revision) string {
	if lc := loggingConfigOf(rev); lc != nil && lc.LogGroup != "" {
		return lc.LogGroup
	}
	return "/aws/lambda/web/" + function
}

func loggingConfigOf(rev *revision) *loggingConfig {
	if tc := rev.Service.TelemetryConfig; tc != nil {
		return tc.LoggingConfig
	}
	return nil
}

// appLogLevelFor and sysLogLevelFor are the revision's loggingConfig levels,
// defaulted to INFO.
func appLogLevelFor(rev *revision) string {
	if lc := loggingConfigOf(rev); lc != nil && lc.ApplicationLogLevel != "" {
		return lc.ApplicationLogLevel
	}
	return "INFO"
}

func sysLogLevelFor(rev *revision) string {
	if lc := loggingConfigOf(rev); lc != nil && lc.SystemLogLevel != "" {
		return lc.SystemLogLevel
	}
	return "INFO"
}

// envSpecFor describes the environment to start for a revision.
func (s *Service) envSpecFor(rt *endpointRT, rev *revision, es *envState) envSpec {
	obj := rev.BuildConfig.CodeConfig.S3Object
	mem := s.cfg.LambdaWebEnvironmentMemoryMB
	if mem <= 0 {
		mem = 2048
	}
	return envSpec{
		Region: rt.region, Function: rt.function, Endpoint: rt.endpoint, Revision: rev,
		Code:     codeRef{Bucket: obj.Bucket, Key: obj.Key, VersionID: obj.VersionID},
		Env:      rev.Service.EnvironmentVariables,
		MemoryMB: mem, InitTimeout: initTimeout,
		LogGroup: es.ref.LogGroup, LogStream: es.ref.LogStream,
		AppLogLevel: appLogLevelFor(rev), SysLogLevel: es.ref.SysLogLevel,
	}
}
