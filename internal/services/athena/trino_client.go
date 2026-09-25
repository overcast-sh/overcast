package athena

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// trino_client.go — the client half of Trino's REST protocol
// (https://trino.io/docs/current/develop/client-protocol.html): POST the
// statement to /v1/statement, then GET each nextUri until the response has
// none, and DELETE the current nextUri to cancel. That is the whole protocol
// a single-statement client needs, so there is no driver dependency.

// trinoUser is the user every query runs as. Trino requires one; the
// engine's default access control lets it do anything.
const trinoUser = "overcast"

// trinoCancelTimeout bounds the DELETE that cancels a query, which runs after
// the query's own context is already done.
const trinoCancelTimeout = 5 * time.Second

// trinoSession is the session a statement runs in: Athena's
// QueryExecutionContext, as Trino's catalog and schema.
type trinoSession struct {
	Catalog string
	Schema  string
}

// trinoTypeSignature is a column type in structured form, which is what
// type mapping and value formatting read.
type trinoTypeSignature struct {
	RawType   string              `json:"rawType"`
	Arguments []trinoTypeArgument `json:"arguments"`
}

// trinoTypeArgument is one type parameter: a LONG (a length, precision or
// scale), a TYPE (an element type) or a NAMED_TYPE (a row field).
type trinoTypeArgument struct {
	Kind  string          `json:"kind"`
	Value json.RawMessage `json:"value"`
}

// trinoNamedType is a row field: its name and type.
type trinoNamedType struct {
	FieldName     *struct{ Name string } `json:"fieldName"`
	TypeSignature trinoTypeSignature     `json:"typeSignature"`
}

type trinoColumn struct {
	Name          string             `json:"name"`
	Type          string             `json:"type"`
	TypeSignature trinoTypeSignature `json:"typeSignature"`
}

// trinoStats is the subset of a query's statistics Overcast reports.
type trinoStats struct {
	State                string `json:"state"`
	Queued               bool   `json:"queued"`
	ElapsedTimeMillis    int64  `json:"elapsedTimeMillis"`
	QueuedTimeMillis     int64  `json:"queuedTimeMillis"`
	PlanningTimeMillis   int64  `json:"planningTimeMillis"`
	ProcessedRows        int64  `json:"processedRows"`
	ProcessedBytes       int64  `json:"processedBytes"`
	PhysicalInputBytes   int64  `json:"physicalInputBytes"`
	PhysicalWrittenBytes int64  `json:"physicalWrittenBytes"`
}

// trinoError is a failed query's error.
type trinoError struct {
	Message   string `json:"message"`
	ErrorCode int    `json:"errorCode"`
	ErrorName string `json:"errorName"`
	ErrorType string `json:"errorType"`
}

func (e *trinoError) Error() string { return e.ErrorName + ": " + e.Message }

// trinoResponse is one page of the protocol.
type trinoResponse struct {
	ID          string          `json:"id"`
	NextURI     string          `json:"nextUri"`
	Columns     []trinoColumn   `json:"columns"`
	Data        [][]any         `json:"data"`
	Stats       trinoStats      `json:"stats"`
	Error       *trinoError     `json:"error"`
	UpdateType  string          `json:"updateType"`
	UpdateCount *int64          `json:"updateCount"`
	Warnings    json.RawMessage `json:"warnings"`
}

// trinoPageHandler sees each page as it arrives. Returning an error stops
// the query: it is cancelled on the engine and the error returned.
type trinoPageHandler func(page *trinoResponse) error

// errTrinoUnreachable wraps a failure to reach the engine at all, as opposed
// to a query the engine ran and failed.
var errTrinoUnreachable = errors.New("athena: query engine unreachable")

type trinoClient struct {
	http *http.Client
}

func newTrinoClient() *trinoClient {
	return &trinoClient{http: &http.Client{}}
}

// execute runs sql at baseURL and hands each page to onPage. It returns the
// last page, which carries the final statistics and, for a statement that
// changed data, its update type and count. A query the engine failed comes
// back as a *trinoError; ctx ending cancels the query on the engine.
func (c *trinoClient) execute(ctx context.Context, baseURL, sql string, session trinoSession, onPage trinoPageHandler) (*trinoResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(baseURL, "/")+"/v1/statement", strings.NewReader(sql))
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Trino-User", trinoUser)
	req.Header.Set("X-Trino-Source", "overcast-athena")
	if session.Catalog != "" {
		req.Header.Set("X-Trino-Catalog", session.Catalog)
	}
	if session.Schema != "" {
		req.Header.Set("X-Trino-Schema", session.Schema)
	}
	page, err := c.do(req)
	for err == nil {
		if herr := onPage(page); herr != nil {
			c.cancel(page.NextURI)
			return page, herr
		}
		if page.Error != nil {
			return page, page.Error
		}
		if page.NextURI == "" {
			return page, nil
		}
		next := page.NextURI
		if req, err = http.NewRequestWithContext(ctx, http.MethodGet, next, nil); err != nil {
			break
		}
		if page, err = c.do(req); err != nil && ctx.Err() != nil {
			c.cancel(next)
			return nil, ctx.Err()
		}
	}
	return nil, err
}

// do sends one protocol request and decodes its page. Numbers are kept as
// written, so a bigint or a decimal is never rounded through a float64.
func (c *trinoClient) do(req *http.Request) (*trinoResponse, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		if req.Context().Err() != nil {
			return nil, req.Context().Err()
		}
		return nil, fmt.Errorf("%w: %v", errTrinoUnreachable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("%w: %s %s: HTTP %d: %s", errTrinoUnreachable, req.Method, req.URL.Path, resp.StatusCode, bytes.TrimSpace(body))
	}
	dec := json.NewDecoder(resp.Body)
	dec.UseNumber()
	var page trinoResponse
	if err := dec.Decode(&page); err != nil {
		return nil, fmt.Errorf("athena: decode engine response: %w", err)
	}
	return &page, nil
}

// cancel abandons a query at its current nextUri. Best effort: the query is
// already over as far as Athena is concerned.
func (c *trinoClient) cancel(nextURI string) {
	if nextURI == "" {
		return
	}
	ctx, done := context.WithTimeout(context.Background(), trinoCancelTimeout)
	defer done()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, nextURI, nil)
	if err != nil {
		return
	}
	if resp, err := c.http.Do(req); err == nil {
		resp.Body.Close()
	}
}

// info is GET /v1/info: whether the engine has finished starting.
func (c *trinoClient) info(ctx context.Context, baseURL string) (starting bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(baseURL, "/")+"/v1/info", nil)
	if err != nil {
		return false, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var body struct {
		Starting bool `json:"starting"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return false, err
	}
	return body.Starting, nil
}
