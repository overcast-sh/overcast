package events

// Lambda Web Functions events. They let the console and the system map follow
// a web function, its revisions and its endpoints as they change instead of
// polling for them.

const (
	// LambdaWebFunctionCreated fires after a web function is created, by
	// CreateWebFunction or CloudFormation. Payload: LambdaWebPayload.
	LambdaWebFunctionCreated Type = "lambda-web:FunctionCreated"
	// LambdaWebFunctionDeleted fires after DeleteWebFunction removes a web
	// function with its revisions and endpoints. Payload: LambdaWebPayload.
	LambdaWebFunctionDeleted Type = "lambda-web:FunctionDeleted"
	// LambdaWebRevisionCreated fires after a revision is created, including the
	// first revision CreateWebFunction makes. Payload: LambdaWebPayload, with
	// Revision set.
	LambdaWebRevisionCreated Type = "lambda-web:RevisionCreated"
	// LambdaWebRevisionDeleted fires after a revision is deleted, by
	// DeleteWebFunctionRevision or by the cleanup that keeps a function to 50
	// revisions. Payload: LambdaWebPayload, with Revision set.
	LambdaWebRevisionDeleted Type = "lambda-web:RevisionDeleted"
	// LambdaWebEndpointCreated fires after an endpoint is created, including
	// the first endpoint CreateWebFunction makes. Payload: LambdaWebPayload,
	// with Endpoint set.
	LambdaWebEndpointCreated Type = "lambda-web:EndpointCreated"
	// LambdaWebEndpointUpdated fires after an endpoint's configuration or its
	// revision weights change, including when a new revision is routed to it
	// by LatestRevision. Payload: LambdaWebPayload, with Endpoint set.
	LambdaWebEndpointUpdated Type = "lambda-web:EndpointUpdated"
	// LambdaWebEndpointDeleted fires after DeleteWebFunctionEndpoint removes an
	// endpoint. Payload: LambdaWebPayload, with Endpoint set.
	LambdaWebEndpointDeleted Type = "lambda-web:EndpointDeleted"
	// LambdaWebEnvironmentStarted fires when an execution environment finished
	// initialising and began serving. Payload: LambdaWebEnvironmentPayload.
	LambdaWebEnvironmentStarted Type = "lambda-web:EnvironmentStarted"
	// LambdaWebEnvironmentStopped fires when an execution environment has shut
	// down, for any reason. Payload: LambdaWebEnvironmentPayload, with Reason set.
	LambdaWebEnvironmentStopped Type = "lambda-web:EnvironmentStopped"
	// LambdaWebRequestCompleted fires for requests that reached an execution
	// environment. It is high volume, so it is classified as noise and
	// coalesced: at most one event per endpoint per second, whose Count says
	// how many requests it stands for. Payload: LambdaWebRequestPayload.
	LambdaWebRequestCompleted Type = "lambda-web:RequestCompleted"
)

// LambdaWebPayload names the web function a lambda-web event is about, and the
// revision or endpoint when it is about one.
type LambdaWebPayload struct {
	Function    string `json:"function"`
	FunctionARN string `json:"functionArn"`
	Region      string `json:"region"`
	Revision    string `json:"revision,omitempty"`
	Endpoint    string `json:"endpoint,omitempty"`
}

// LambdaWebEnvironmentPayload describes an execution environment lifecycle
// change. Endpoint is the endpoint whose traffic triggered the start. Reason
// is "initialized" on start and "requested", "oom" or "exited" on stop.
type LambdaWebEnvironmentPayload struct {
	Function    string `json:"function"`
	Region      string `json:"region"`
	Revision    string `json:"revision"`
	Environment string `json:"environment"`
	Endpoint    string `json:"endpoint,omitempty"`
	Reason      string `json:"reason"`
}

// LambdaWebRequestPayload describes a request that reached an environment.
// When requests were coalesced, Status, DurationMs and Environment are the
// last request's, ColdStart is true if any of them was a cold start, and Count
// is how many requests the event stands for (1 when nothing was coalesced).
type LambdaWebRequestPayload struct {
	Function    string  `json:"function"`
	Region      string  `json:"region"`
	Endpoint    string  `json:"endpoint"`
	Revision    string  `json:"revision"`
	Environment string  `json:"environment"`
	Status      int     `json:"status"`
	DurationMs  float64 `json:"durationMs"`
	ColdStart   bool    `json:"coldStart"`
	Count       int     `json:"count"`
}
