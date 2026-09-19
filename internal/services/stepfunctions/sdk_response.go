package stepfunctions

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"time"

	cborlib "github.com/fxamacker/cbor/v2"

	"github.com/overcast-sh/overcast/internal/awsapi"
	"github.com/overcast-sh/overcast/internal/awsshapes"
)

// Reading a service's wire response back into the JSON an aws-sdk Task
// produces. Every protocol walks the operation's output shape, so a result
// carries exactly the modeled members, each under its PascalCase name, while
// map keys — which are data, not member names — pass through untouched.
//
// The switches over shape kinds (and protocols) below name the kinds that need
// their own handling and let every other kind fall through to the scalar path
// after them, so they are marked //exhaustive:ignore.

// decodeResponse reads a successful response.
func (c *sdkCall) decodeResponse(rec *httptest.ResponseRecorder) (any, error) {
	output := c.shape.Output
	if output == nil || output.Kind == awsshapes.KindUnit {
		output = &awsshapes.Shape{Kind: awsshapes.KindStructure}
	}
	body := rec.Body.Bytes()
	//exhaustive:ignore
	switch c.protocol {
	case awsapi.ProtocolAWSJSON10, awsapi.ProtocolAWSJSON11:
		v, err := parseJSONBody(body)
		if err != nil {
			return nil, err
		}
		return jsonResult(output, nil, v, false, nil), nil
	case awsapi.ProtocolRPCV2CBOR:
		if len(body) == 0 {
			return map[string]any{}, nil
		}
		var v any
		if err := cborResultMode.Unmarshal(body, &v); err != nil {
			return nil, err
		}
		return cborResult(output, v), nil
	case awsapi.ProtocolAWSQuery:
		root, err := parseXMLTree(body)
		if err != nil {
			return nil, err
		}
		result := root.child(c.op.Name + "Result")
		if result == nil {
			return map[string]any{}, nil
		}
		return xmlResult(output, nil, result, nil), nil
	case awsapi.ProtocolEC2Query:
		root, err := parseXMLTree(body)
		if err != nil {
			return nil, err
		}
		return xmlResult(output, nil, root, nil), nil
	}
	return c.restResult(output, rec)
}

// cborResultMode decodes maps as map[string]any, as the server-side codec does.
var cborResultMode = func() cborlib.DecMode {
	mode, err := cborlib.DecOptions{DefaultMapType: reflect.TypeOf(map[string]any(nil))}.DecMode()
	if err != nil {
		panic("stepfunctions: building CBOR decode mode: " + err.Error())
	}
	return mode
}()

func parseJSONBody(body []byte) (any, error) {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return map[string]any{}, nil
	}
	var v any
	err := json.Unmarshal(body, &v)
	return v, err
}

// restResult assembles a REST response from its status, headers and body.
func (c *sdkCall) restResult(output *awsshapes.Shape, rec *httptest.ResponseRecorder) (any, error) {
	out := map[string]any{}
	body := rec.Body.Bytes()
	bodyBound := false
	for _, m := range output.Members {
		name := capitalize(m.Name)
		switch {
		case m.Header != "":
			raw := strings.Join(rec.Header().Values(m.Header), ", ")
			if raw == "" {
				continue
			}
			out[name] = headerResult(m, raw)
		case m.PrefixHeader != nil:
			prefix := strings.ToLower(*m.PrefixHeader)
			values := map[string]any{}
			for header, vals := range rec.Header() {
				lower := strings.ToLower(header)
				if prefix != "" && strings.HasPrefix(lower, prefix) && len(vals) > 0 {
					values[lower[len(prefix):]] = vals[0]
				}
			}
			if len(values) > 0 {
				out[name] = values
			}
		case m.ResponseCode:
			out[name] = float64(rec.Code)
		case m.Payload:
			bodyBound = true
			//exhaustive:ignore
			switch m.Target.Kind {
			case awsshapes.KindBlob, awsshapes.KindString, awsshapes.KindEnum:
				out[name] = string(body)
			case awsshapes.KindDocument:
				v, err := parseJSONBody(body)
				if err != nil {
					return nil, err
				}
				out[name] = v
			default:
				if len(bytes.TrimSpace(body)) == 0 {
					continue
				}
				v, err := c.restBody(m.Target, m, body, nil)
				if err != nil {
					return nil, err
				}
				out[name] = v
			}
		}
	}
	if !bodyBound && hasBodyMembers(output) && len(bytes.TrimSpace(body)) > 0 {
		v, err := c.restBody(output, nil, body, isBound)
		if err != nil {
			return nil, err
		}
		if obj, ok := v.(map[string]any); ok {
			for k, val := range obj {
				out[k] = val
			}
		}
	}
	return out, nil
}

// isBound reports whether a REST member travels outside the body.
func isBound(m *awsshapes.Member) bool {
	return m.Label || m.Query != "" || m.QueryParams || m.Header != "" || m.PrefixHeader != nil || m.Payload || m.ResponseCode
}

// restBody decodes a REST body — JSON or XML — as target.
func (c *sdkCall) restBody(target *awsshapes.Shape, member *awsshapes.Member, body []byte, skip func(*awsshapes.Member) bool) (any, error) {
	if c.protocol == awsapi.ProtocolRESTJSON {
		v, err := parseJSONBody(body)
		if err != nil {
			return nil, err
		}
		return jsonResult(target, member, v, true, skip), nil
	}
	root, err := parseXMLTree(body)
	if err != nil {
		return nil, err
	}
	return xmlResult(target, member, root, skip), nil
}

// headerResult reads a header-bound member.
func headerResult(m *awsshapes.Member, raw string) any {
	target := m.Target
	if target.Kind == awsshapes.KindList {
		parts := strings.Split(raw, ",")
		out := make([]any, 0, len(parts))
		for _, part := range parts {
			out = append(out, textResult(target.Member, nil, strings.TrimSpace(part)))
		}
		return out
	}
	return textResult(target, m, raw)
}

// textResult converts wire text (XML, headers) to a result value.
func textResult(target *awsshapes.Shape, member *awsshapes.Member, text string) any {
	//exhaustive:ignore
	switch target.Kind {
	case awsshapes.KindBoolean:
		b, err := strconv.ParseBool(strings.TrimSpace(text))
		if err != nil {
			return text
		}
		return b
	case awsshapes.KindTimestamp:
		return timestampResult(text)
	}
	if target.Kind.IsNumber() {
		if f, err := strconv.ParseFloat(strings.TrimSpace(text), 64); err == nil {
			return f
		}
	}
	return text
}

// ─── JSON ─────────────────────────────────────────────────────────────────────

// jsonResult converts decoded JSON through the output shape. rest selects
// REST-JSON's @jsonName wire names; skip omits members bound elsewhere.
func jsonResult(target *awsshapes.Shape, member *awsshapes.Member, v any, rest bool, skip func(*awsshapes.Member) bool) any {
	if v == nil {
		return nil
	}
	//exhaustive:ignore
	switch target.Kind {
	case awsshapes.KindStructure, awsshapes.KindUnion:
		obj, ok := v.(map[string]any)
		if !ok {
			return v
		}
		out := make(map[string]any, len(obj))
		for _, m := range target.Members {
			if skip != nil && skip(m) {
				continue
			}
			wire := m.Name
			if rest && m.JSONName != "" {
				wire = m.JSONName
			}
			if val, ok := obj[wire]; ok && val != nil {
				out[capitalize(m.Name)] = jsonResult(m.Target, m, val, rest, nil)
			}
		}
		return out
	case awsshapes.KindList:
		arr, ok := v.([]any)
		if !ok {
			return v
		}
		out := make([]any, len(arr))
		for i, el := range arr {
			out[i] = jsonResult(target.Member, nil, el, rest, nil)
		}
		return out
	case awsshapes.KindMap:
		obj, ok := v.(map[string]any)
		if !ok {
			return v
		}
		out := make(map[string]any, len(obj))
		for key, el := range obj {
			out[key] = jsonResult(target.Value, nil, el, rest, nil)
		}
		return out
	case awsshapes.KindTimestamp:
		return timestampResult(v)
	}
	return v
}

// ─── CBOR ─────────────────────────────────────────────────────────────────────

func cborResult(target *awsshapes.Shape, v any) any {
	if v == nil {
		return nil
	}
	//exhaustive:ignore
	switch target.Kind {
	case awsshapes.KindStructure, awsshapes.KindUnion:
		obj, ok := v.(map[string]any)
		if !ok {
			return v
		}
		out := make(map[string]any, len(obj))
		for _, m := range target.Members {
			if val, ok := obj[m.Name]; ok && val != nil {
				out[capitalize(m.Name)] = cborResult(m.Target, val)
			}
		}
		return out
	case awsshapes.KindList:
		arr, ok := v.([]any)
		if !ok {
			return v
		}
		out := make([]any, len(arr))
		for i, el := range arr {
			out[i] = cborResult(target.Member, el)
		}
		return out
	case awsshapes.KindMap:
		obj, ok := v.(map[string]any)
		if !ok {
			return v
		}
		out := make(map[string]any, len(obj))
		for key, el := range obj {
			out[key] = cborResult(target.Value, el)
		}
		return out
	case awsshapes.KindTimestamp:
		switch x := v.(type) {
		case time.Time:
			return javaInstant(x)
		case cborlib.Tag:
			return timestampResult(cborNumber(x.Content))
		}
		return timestampResult(cborNumber(v))
	case awsshapes.KindBlob:
		if b, ok := v.([]byte); ok {
			return base64.StdEncoding.EncodeToString(b)
		}
		return v
	}
	if target.Kind.IsNumber() {
		return cborNumber(v)
	}
	return v
}

// cborNumber normalises a CBOR number to float64, as JSON numbers decode.
func cborNumber(v any) any {
	switch x := v.(type) {
	case uint64:
		return float64(x)
	case int64:
		return float64(x)
	case float32:
		return float64(x)
	}
	return v
}

// ─── XML (AWS Query, EC2 Query, REST-XML) ─────────────────────────────────────

type xmlNode struct {
	name     string
	attrs    map[string]string
	children []*xmlNode
	text     string
}

func (n *xmlNode) child(name string) *xmlNode {
	for _, c := range n.children {
		if c.name == name {
			return c
		}
	}
	return nil
}

func (n *xmlNode) find(name string) *xmlNode {
	if n.name == name {
		return n
	}
	for _, c := range n.children {
		if found := c.find(name); found != nil {
			return found
		}
	}
	return nil
}

// parseXMLTree parses a document into a tree of local element names.
func parseXMLTree(body []byte) (*xmlNode, error) {
	dec := xml.NewDecoder(bytes.NewReader(body))
	var stack []*xmlNode
	var root *xmlNode
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			node := &xmlNode{name: t.Name.Local}
			for _, a := range t.Attr {
				if node.attrs == nil {
					node.attrs = map[string]string{}
				}
				node.attrs[a.Name.Local] = a.Value
			}
			if len(stack) > 0 {
				parent := stack[len(stack)-1]
				parent.children = append(parent.children, node)
			} else if root == nil {
				root = node
			}
			stack = append(stack, node)
		case xml.EndElement:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		case xml.CharData:
			if len(stack) > 0 {
				stack[len(stack)-1].text += string(t)
			}
		}
	}
	if root == nil {
		return nil, errors.New("the response has no XML document")
	}
	return root, nil
}

// xmlResult converts an element through the output shape.
func xmlResult(target *awsshapes.Shape, member *awsshapes.Member, node *xmlNode, skip func(*awsshapes.Member) bool) any {
	//exhaustive:ignore
	switch target.Kind {
	case awsshapes.KindStructure, awsshapes.KindUnion:
		out := map[string]any{}
		for _, m := range target.Members {
			if skip != nil && skip(m) {
				continue
			}
			name := orDefault(m.XMLName, m.Name)
			if m.XMLAttribute {
				if v, ok := node.attrs[name]; ok {
					out[capitalize(m.Name)] = textResult(m.Target, m, v)
				}
				continue
			}
			if v, ok := xmlMember(m, name, node); ok {
				out[capitalize(m.Name)] = v
			}
		}
		return out
	case awsshapes.KindList:
		out := make([]any, 0, len(node.children))
		for _, item := range node.children {
			out = append(out, xmlResult(target.Member, nil, item, nil))
		}
		return out
	case awsshapes.KindMap:
		return xmlMapEntries(target, node.children)
	}
	return textResult(target, member, strings.TrimSpace(node.text))
}

// xmlMember reads one member from its parent element, honouring flattening.
func xmlMember(m *awsshapes.Member, name string, parent *xmlNode) (any, bool) {
	target := m.Target
	if m.XMLFlattened && (target.Kind == awsshapes.KindList || target.Kind == awsshapes.KindMap) {
		var items []*xmlNode
		for _, c := range parent.children {
			if c.name == name {
				items = append(items, c)
			}
		}
		if len(items) == 0 {
			return nil, false
		}
		if target.Kind == awsshapes.KindMap {
			return xmlMapEntries(target, items), true
		}
		out := make([]any, len(items))
		for i, item := range items {
			out[i] = xmlResult(target.Member, nil, item, nil)
		}
		return out, true
	}
	child := parent.child(name)
	if child == nil {
		return nil, false
	}
	return xmlResult(target, m, child, nil), true
}

func xmlMapEntries(target *awsshapes.Shape, entries []*xmlNode) map[string]any {
	keyName, valueName := orDefault(target.KeyXMLName, "key"), orDefault(target.ValueXMLName, "value")
	out := make(map[string]any, len(entries))
	for _, entry := range entries {
		key := entry.child(keyName)
		value := entry.child(valueName)
		if key == nil || value == nil {
			continue
		}
		out[strings.TrimSpace(key.text)] = xmlResult(target.Value, nil, value, nil)
	}
	return out
}

// ─── Timestamps ───────────────────────────────────────────────────────────────

// timestampResult renders a wire timestamp — epoch seconds, ISO-8601 or an
// HTTP date — as AWS's SDK integrations do: an ISO-8601 instant.
func timestampResult(v any) any {
	switch x := v.(type) {
	case float64:
		return javaInstant(epochTime(x))
	case json.Number:
		if f, err := x.Float64(); err == nil {
			return javaInstant(epochTime(f))
		}
	case string:
		if t, ok := parseTimestamp(x); ok {
			return javaInstant(t)
		}
	}
	return v
}

// parseTimestamp reads the textual timestamp forms AWS protocols use.
func parseTimestamp(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02T15:04:05.999999999Z0700"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), true
		}
	}
	if t, err := http.ParseTime(s); err == nil {
		return t.UTC(), true
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil && !math.IsInf(f, 0) && !math.IsNaN(f) {
		return epochTime(f), true
	}
	return time.Time{}, false
}

// javaInstant formats as java.time.Instant#toString does, which is what AWS's
// SDK integrations emit: UTC, and a fraction only when there is one, in
// groups of three digits.
func javaInstant(t time.Time) string {
	t = t.UTC()
	base := t.Format("2006-01-02T15:04:05")
	nanos := t.Nanosecond()
	switch {
	case nanos == 0:
		return base + "Z"
	case nanos%1_000_000 == 0:
		return base + "." + leftPad(nanos/1_000_000, 3) + "Z"
	case nanos%1_000 == 0:
		return base + "." + leftPad(nanos/1_000, 6) + "Z"
	}
	return base + "." + leftPad(nanos, 9) + "Z"
}

func leftPad(n, width int) string {
	s := strconv.Itoa(n)
	for len(s) < width {
		s = "0" + s
	}
	return s
}

// ─── Errors ───────────────────────────────────────────────────────────────────

// errorDetails reads the code, message and request ID from an error response
// in whichever envelope the service answered with.
func (c *sdkCall) errorDetails(rec *httptest.ResponseRecorder) (code, message, requestID string) {
	header := rec.Header()
	for _, name := range []string{"X-Amzn-Requestid", "X-Amz-Request-Id", "X-Amzn-Request-Id"} {
		if v := header.Get(name); v != "" {
			requestID = v
			break
		}
	}
	if v := header.Get("X-Amzn-Errortype"); v != "" {
		code, _, _ = strings.Cut(v, ":")
	}
	body := bytes.TrimSpace(rec.Body.Bytes())
	switch {
	case strings.HasPrefix(header.Get("Content-Type"), "application/cbor"):
		var v map[string]any
		if cborResultMode.Unmarshal(body, &v) == nil {
			if code == "" {
				code, _ = v["__type"].(string)
			}
			message = firstString(v, "message", "Message")
		}
	case len(body) > 0 && body[0] == '{':
		var v map[string]any
		if json.Unmarshal(body, &v) == nil {
			if code == "" {
				code = firstString(v, "__type", "code", "Code")
			}
			message = firstString(v, "message", "Message", "errorMessage")
		}
	case len(body) > 0 && body[0] == '<':
		if root, err := parseXMLTree(body); err == nil {
			if n := root.find("Code"); n != nil && code == "" {
				code = strings.TrimSpace(n.text)
			}
			if n := root.find("Message"); n != nil {
				message = strings.TrimSpace(n.text)
			}
			if requestID == "" {
				if n := root.find("RequestId"); n != nil {
					requestID = strings.TrimSpace(n.text)
				} else if n := root.find("RequestID"); n != nil {
					requestID = strings.TrimSpace(n.text)
				}
			}
		}
	}
	if hash := strings.LastIndex(code, "#"); hash >= 0 {
		code = code[hash+1:]
	}
	if code == "" {
		// A HEAD response carries no body: the status is all there is, and
		// these are the codes the models give those statuses.
		switch rec.Code {
		case http.StatusNotFound:
			code = "NotFound"
		case http.StatusForbidden:
			code = "Forbidden"
		case http.StatusBadRequest:
			code = "BadRequest"
		case http.StatusNotModified:
			code = "NotModified"
		case http.StatusPreconditionFailed:
			code = "PreconditionFailed"
		}
	}
	return code, message, requestID
}

func firstString(v map[string]any, keys ...string) string {
	for _, key := range keys {
		if s, ok := v[key].(string); ok && s != "" {
			return s
		}
	}
	return ""
}
