package stepfunctions

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	cborlib "github.com/fxamacker/cbor/v2"

	"github.com/overcast-sh/overcast/internal/awsapi"
	"github.com/overcast-sh/overcast/internal/awsshapes"
)

// Serialising an aws-sdk Task's parameters into the wire request its service
// expects. Every protocol walks the operation's input shape, so parameter
// names are matched to members (PascalCase, as AWS spells SDK parameters) and
// values are coerced to the member's type — nothing here knows any service.
//
// The switches over shape kinds (and protocols) below name the kinds that need
// their own handling and let every other kind fall through to the scalar path
// after them, so they are marked //exhaustive:ignore.

// buildRequest renders the call as an HTTP request for the router.
func (c *sdkCall) buildRequest(ctx context.Context, params map[string]any) (*http.Request, error) {
	input := c.shape.Input
	if input == nil || input.Kind == awsshapes.KindUnit {
		input = &awsshapes.Shape{Kind: awsshapes.KindStructure}
	}
	// A top-level field that names no member is dropped rather than refused.
	// Static Parameters are checked against the members at CreateStateMachine
	// (validateSDKTask), so what reaches here unchecked is a Task with no
	// Parameters, whose whole state input is the request — and that input
	// routinely carries fields meant for other states. Nested structures stay
	// strict: a mistyped member there would otherwise vanish silently.
	known := make(map[string]any, len(params))
	for key, value := range params {
		if sdkMember(input, key) != nil {
			known[key] = value
		}
	}
	params = known
	//exhaustive:ignore
	switch c.protocol {
	case awsapi.ProtocolAWSJSON10, awsapi.ProtocolAWSJSON11:
		body, err := jsonValue(input, nil, params, false, "")
		if err != nil {
			return nil, err
		}
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "/", bytes.NewReader(encoded))
		if err != nil {
			return nil, err
		}
		contentType := "application/x-amz-json-1.0"
		if c.protocol == awsapi.ProtocolAWSJSON11 {
			contentType = "application/x-amz-json-1.1"
		}
		req.Header.Set("Content-Type", contentType)
		req.Header.Set("X-Amz-Target", c.op.TargetPrefix+c.op.Name)
		return req, nil
	case awsapi.ProtocolRPCV2CBOR:
		body, err := cborValue(input, nil, params, "")
		if err != nil {
			return nil, err
		}
		encoded, err := cborlib.Marshal(body)
		if err != nil {
			return nil, err
		}
		path := "/service/" + c.op.ServiceShape + "/operation/" + c.op.Name
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, path, bytes.NewReader(encoded))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Smithy-Protocol", "rpc-v2-cbor")
		req.Header.Set("Content-Type", "application/cbor")
		req.Header.Set("Accept", "application/cbor")
		return req, nil
	case awsapi.ProtocolAWSQuery, awsapi.ProtocolEC2Query:
		form := url.Values{}
		form.Set("Action", c.op.Name)
		form.Set("Version", c.op.APIVersion)
		enc := queryEncoder{form: form, ec2: c.protocol == awsapi.ProtocolEC2Query}
		if err := enc.encode("", input, nil, params, ""); err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "/", strings.NewReader(form.Encode()))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
		return req, nil
	case awsapi.ProtocolRESTJSON, awsapi.ProtocolRESTXML:
		return c.restRequest(ctx, input, params)
	}
	return nil, fmt.Errorf("protocol %s is not supported", c.protocol)
}

// ─── Parameter coercion ───────────────────────────────────────────────────────

func paramError(path, want string, v any) error {
	return fmt.Errorf("Parameters%s must be %s, not %s", path, want, jsonKind(v))
}

func jsonKind(v any) string {
	switch v.(type) {
	case map[string]any:
		return "an object"
	case []any:
		return "an array"
	case string:
		return "a string"
	case bool:
		return "a boolean"
	case nil:
		return "null"
	}
	return "a number"
}

// paramString renders a string member. A non-string value is accepted and
// written as its JSON text, which is how a Body or MessageBody given as an
// object reaches the service.
func paramString(v any) (string, error) {
	switch x := v.(type) {
	case string:
		return x, nil
	case json.Number:
		return x.String(), nil
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64), nil
	case int:
		return strconv.Itoa(x), nil
	case bool:
		return strconv.FormatBool(x), nil
	}
	encoded, err := json.Marshal(v)
	return string(encoded), err
}

func paramBool(v any, path string) (bool, error) {
	switch x := v.(type) {
	case bool:
		return x, nil
	case string:
		if b, err := strconv.ParseBool(x); err == nil {
			return b, nil
		}
	}
	return false, paramError(path, "a boolean", v)
}

// paramNumber returns a number member's canonical text.
func paramNumber(v any, kind awsshapes.Kind, path string) (string, error) {
	var text string
	switch x := v.(type) {
	case json.Number:
		text = x.String()
	case float64:
		text = strconv.FormatFloat(x, 'f', -1, 64)
	case int:
		text = strconv.Itoa(x)
	case string:
		text = strings.TrimSpace(x)
	default:
		return "", paramError(path, "a number", v)
	}
	f, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return "", paramError(path, "a number", v)
	}
	//exhaustive:ignore
	switch kind {
	case awsshapes.KindByte, awsshapes.KindShort, awsshapes.KindInteger, awsshapes.KindLong:
		if f != float64(int64(f)) {
			return "", fmt.Errorf("Parameters%s must be an integer, not %s", path, text)
		}
		return strconv.FormatInt(int64(f), 10), nil
	}
	return text, nil
}

// paramTime accepts an ISO-8601 string or epoch seconds.
func paramTime(v any, path string) (time.Time, error) {
	switch x := v.(type) {
	case string:
		if t, ok := parseTimestamp(x); ok {
			return t, nil
		}
	case float64, json.Number, int:
		f, _ := toNumber(x)
		return epochTime(f), nil
	}
	return time.Time{}, paramError(path, "an ISO-8601 timestamp or epoch seconds", v)
}

// paramBlob returns a blob member's bytes. A payload blob — an object body,
// a Lambda payload — is its text (an object or array is JSON-encoded). Any
// other blob is base64 on every AWS wire and in AWS's SDK integration
// results, so it is taken as base64, falling back to the raw text when it is
// not valid base64.
func paramBlob(v any, payload bool) ([]byte, error) {
	text, err := paramString(v)
	if err != nil {
		return nil, err
	}
	if !payload {
		if decoded, err := base64.StdEncoding.DecodeString(text); err == nil {
			return decoded, nil
		}
	}
	return []byte(text), nil
}

func epochTime(f float64) time.Time {
	sec := int64(f)
	return time.Unix(sec, int64((f-float64(sec))*1e9)).UTC()
}

func epochText(t time.Time) string {
	return strconv.FormatFloat(float64(t.UnixNano())/1e9, 'f', -1, 64)
}

// timestampFormat is the member's or shape's @timestampFormat, else def.
func timestampFormat(target *awsshapes.Shape, member *awsshapes.Member, def string) string {
	if member != nil && member.TimestampFormat != "" {
		return member.TimestampFormat
	}
	if target.TimestampFormat != "" {
		return target.TimestampFormat
	}
	return def
}

func formatTimeText(t time.Time, format string) string {
	switch format {
	case "epoch-seconds":
		return epochText(t)
	case "http-date":
		return t.UTC().Format(http.TimeFormat)
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// structureParams checks a structure value and resolves each key to a member.
func structureParams(shape *awsshapes.Shape, v any, path string) ([]*awsshapes.Member, []any, error) {
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, nil, paramError(path, "an object", v)
	}
	keys := make([]string, 0, len(obj))
	for key := range obj {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	members := make([]*awsshapes.Member, 0, len(keys))
	values := make([]any, 0, len(keys))
	for _, key := range keys {
		if obj[key] == nil {
			continue
		}
		m := sdkMember(shape, key)
		if m == nil {
			return nil, nil, fmt.Errorf("the field %q is not supported by Step Functions at Parameters%s", key, path)
		}
		members = append(members, m)
		values = append(values, obj[key])
	}
	return members, values, nil
}

// ─── JSON (AWS JSON 1.0/1.1 and REST-JSON bodies) ────────────────────────────

// jsonValue converts a parameter to the JSON a service expects. rest selects
// REST-JSON's @jsonName member names; AWS JSON ignores the trait.
func jsonValue(target *awsshapes.Shape, member *awsshapes.Member, v any, rest bool, path string) (any, error) {
	//exhaustive:ignore
	switch target.Kind {
	case awsshapes.KindStructure, awsshapes.KindUnion:
		members, values, err := structureParams(target, v, path)
		if err != nil {
			return nil, err
		}
		out := make(map[string]any, len(members))
		for i, m := range members {
			name := m.Name
			if rest && m.JSONName != "" {
				name = m.JSONName
			}
			if out[name], err = jsonValue(m.Target, m, values[i], rest, path+"."+capitalize(m.Name)); err != nil {
				return nil, err
			}
		}
		return out, nil
	case awsshapes.KindList:
		arr, ok := v.([]any)
		if !ok {
			return nil, paramError(path, "an array", v)
		}
		out := make([]any, len(arr))
		for i, el := range arr {
			var err error
			if out[i], err = jsonValue(target.Member, nil, el, rest, path+"["+strconv.Itoa(i)+"]"); err != nil {
				return nil, err
			}
		}
		return out, nil
	case awsshapes.KindMap:
		obj, ok := v.(map[string]any)
		if !ok {
			return nil, paramError(path, "an object", v)
		}
		out := make(map[string]any, len(obj))
		for key, el := range obj {
			var err error
			if out[key], err = jsonValue(target.Value, nil, el, rest, path+"."+key); err != nil {
				return nil, err
			}
		}
		return out, nil
	case awsshapes.KindBoolean:
		return paramBool(v, path)
	case awsshapes.KindBlob:
		b, err := paramBlob(v, false)
		return base64.StdEncoding.EncodeToString(b), err
	case awsshapes.KindTimestamp:
		t, err := paramTime(v, path)
		if err != nil {
			return nil, err
		}
		format := timestampFormat(target, member, "epoch-seconds")
		if format == "epoch-seconds" {
			return json.Number(epochText(t)), nil
		}
		return formatTimeText(t, format), nil
	case awsshapes.KindDocument:
		return v, nil
	}
	if target.Kind.IsNumber() {
		text, err := paramNumber(v, target.Kind, path)
		return json.Number(text), err
	}
	return paramString(v)
}

// ─── CBOR (Smithy RPC v2) ─────────────────────────────────────────────────────

func cborValue(target *awsshapes.Shape, member *awsshapes.Member, v any, path string) (any, error) {
	//exhaustive:ignore
	switch target.Kind {
	case awsshapes.KindStructure, awsshapes.KindUnion:
		members, values, err := structureParams(target, v, path)
		if err != nil {
			return nil, err
		}
		out := make(map[string]any, len(members))
		for i, m := range members {
			if out[m.Name], err = cborValue(m.Target, m, values[i], path+"."+capitalize(m.Name)); err != nil {
				return nil, err
			}
		}
		return out, nil
	case awsshapes.KindList:
		arr, ok := v.([]any)
		if !ok {
			return nil, paramError(path, "an array", v)
		}
		out := make([]any, len(arr))
		for i, el := range arr {
			var err error
			if out[i], err = cborValue(target.Member, nil, el, path+"["+strconv.Itoa(i)+"]"); err != nil {
				return nil, err
			}
		}
		return out, nil
	case awsshapes.KindMap:
		obj, ok := v.(map[string]any)
		if !ok {
			return nil, paramError(path, "an object", v)
		}
		out := make(map[string]any, len(obj))
		for key, el := range obj {
			var err error
			if out[key], err = cborValue(target.Value, nil, el, path+"."+key); err != nil {
				return nil, err
			}
		}
		return out, nil
	case awsshapes.KindBoolean:
		return paramBool(v, path)
	case awsshapes.KindBlob:
		return paramBlob(v, false)
	case awsshapes.KindTimestamp:
		t, err := paramTime(v, path)
		if err != nil {
			return nil, err
		}
		return cborlib.Tag{Number: 1, Content: float64(t.UnixNano()) / 1e9}, nil
	case awsshapes.KindDocument:
		return v, nil
	case awsshapes.KindByte, awsshapes.KindShort, awsshapes.KindInteger, awsshapes.KindLong:
		text, err := paramNumber(v, target.Kind, path)
		if err != nil {
			return nil, err
		}
		return strconv.ParseInt(text, 10, 64)
	}
	if target.Kind.IsNumber() {
		text, err := paramNumber(v, target.Kind, path)
		if err != nil {
			return nil, err
		}
		return strconv.ParseFloat(text, 64)
	}
	return paramString(v)
}

// ─── AWS Query and EC2 Query ──────────────────────────────────────────────────

type queryEncoder struct {
	form url.Values
	ec2  bool
}

// queryName is a member's key segment: @xmlName on AWS Query; on EC2 Query
// @ec2QueryName, else the capitalised @xmlName, else the capitalised name.
func (q queryEncoder) queryName(m *awsshapes.Member) string {
	if q.ec2 {
		if m.EC2QueryName != "" {
			return m.EC2QueryName
		}
		if m.XMLName != "" {
			return capitalize(m.XMLName)
		}
		return capitalize(m.Name)
	}
	if m.XMLName != "" {
		return m.XMLName
	}
	return m.Name
}

func (q queryEncoder) encode(key string, target *awsshapes.Shape, member *awsshapes.Member, v any, path string) error {
	//exhaustive:ignore
	switch target.Kind {
	case awsshapes.KindStructure, awsshapes.KindUnion:
		members, values, err := structureParams(target, v, path)
		if err != nil {
			return err
		}
		for i, m := range members {
			sub := q.queryName(m)
			if key != "" {
				sub = key + "." + sub
			}
			if err := q.encode(sub, m.Target, m, values[i], path+"."+capitalize(m.Name)); err != nil {
				return err
			}
		}
		return nil
	case awsshapes.KindList:
		arr, ok := v.([]any)
		if !ok {
			return paramError(path, "an array", v)
		}
		if len(arr) == 0 && !q.ec2 {
			q.form.Set(key, "")
			return nil
		}
		for i, el := range arr {
			elemKey := key + "." + strconv.Itoa(i+1)
			if !q.ec2 && (member == nil || !member.XMLFlattened) {
				name := target.MemberXMLName
				if name == "" {
					name = "member"
				}
				elemKey = key + "." + name + "." + strconv.Itoa(i+1)
			}
			if err := q.encode(elemKey, target.Member, nil, el, path+"["+strconv.Itoa(i)+"]"); err != nil {
				return err
			}
		}
		return nil
	case awsshapes.KindMap:
		obj, ok := v.(map[string]any)
		if !ok {
			return paramError(path, "an object", v)
		}
		keys := make([]string, 0, len(obj))
		for k := range obj {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		keyName, valueName := orDefault(target.KeyXMLName, "key"), orDefault(target.ValueXMLName, "value")
		for i, k := range keys {
			base := key + ".entry." + strconv.Itoa(i+1)
			if q.ec2 || (member != nil && member.XMLFlattened) {
				base = key + "." + strconv.Itoa(i+1)
			}
			q.form.Set(base+"."+keyName, k)
			if err := q.encode(base+"."+valueName, target.Value, nil, obj[k], path+"."+k); err != nil {
				return err
			}
		}
		return nil
	}
	text, err := scalarText(target, member, v, "date-time", path)
	if err != nil {
		return err
	}
	q.form.Set(key, text)
	return nil
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// scalarText renders a scalar as text — Query values, XML text, labels,
// query-string values and headers — with tsDefault the location's default
// timestamp format.
func scalarText(target *awsshapes.Shape, member *awsshapes.Member, v any, tsDefault, path string) (string, error) {
	//exhaustive:ignore
	switch target.Kind {
	case awsshapes.KindBoolean:
		b, err := paramBool(v, path)
		return strconv.FormatBool(b), err
	case awsshapes.KindBlob:
		b, err := paramBlob(v, false)
		return base64.StdEncoding.EncodeToString(b), err
	case awsshapes.KindTimestamp:
		t, err := paramTime(v, path)
		if err != nil {
			return "", err
		}
		return formatTimeText(t, timestampFormat(target, member, tsDefault)), nil
	case awsshapes.KindStructure, awsshapes.KindUnion, awsshapes.KindList, awsshapes.KindMap:
		return "", paramError(path, "a scalar", v)
	}
	if target.Kind.IsNumber() {
		return paramNumber(v, target.Kind, path)
	}
	return paramString(v)
}

// ─── REST-JSON and REST-XML ───────────────────────────────────────────────────

var uriLabel = regexp.MustCompile(`\{([^}]+)\}`)

func (c *sdkCall) restRequest(ctx context.Context, input *awsshapes.Shape, params map[string]any) (*http.Request, error) {
	pathTemplate, literalQuery, _ := strings.Cut(c.shape.URI, "?")
	var query []string
	if literalQuery != "" {
		query = append(query, literalQuery)
	}
	labels := map[string]string{}
	header := http.Header{}
	body := map[string]any{}
	var payload *awsshapes.Member
	var payloadValue any

	members, values, err := structureParams(input, params, "")
	if err != nil {
		return nil, err
	}
	for i, m := range members {
		v := values[i]
		path := "." + capitalize(m.Name)
		switch {
		case m.Label:
			if labels[m.Name], err = scalarText(m.Target, m, v, "date-time", path); err != nil {
				return nil, err
			}
		case m.Query != "":
			items := []any{v}
			if m.Target.Kind == awsshapes.KindList {
				arr, ok := v.([]any)
				if !ok {
					return nil, paramError(path, "an array", v)
				}
				items = arr
			}
			for _, item := range items {
				target := m.Target
				if target.Kind == awsshapes.KindList {
					target = target.Member
				}
				text, err := scalarText(target, m, item, "date-time", path)
				if err != nil {
					return nil, err
				}
				query = append(query, url.QueryEscape(m.Query)+"="+url.QueryEscape(text))
			}
		case m.QueryParams:
			obj, ok := v.(map[string]any)
			if !ok {
				return nil, paramError(path, "an object", v)
			}
			for key, raw := range obj {
				text, err := paramString(raw)
				if err != nil {
					return nil, err
				}
				query = append(query, url.QueryEscape(key)+"="+url.QueryEscape(text))
			}
		case m.Header != "":
			if m.Target.Kind == awsshapes.KindList {
				arr, ok := v.([]any)
				if !ok {
					return nil, paramError(path, "an array", v)
				}
				parts := make([]string, 0, len(arr))
				for _, item := range arr {
					text, err := scalarText(m.Target.Member, nil, item, "http-date", path)
					if err != nil {
						return nil, err
					}
					parts = append(parts, text)
				}
				header.Set(m.Header, strings.Join(parts, ", "))
				continue
			}
			text, err := scalarText(m.Target, m, v, "http-date", path)
			if err != nil {
				return nil, err
			}
			header.Set(m.Header, text)
		case m.PrefixHeader != nil:
			obj, ok := v.(map[string]any)
			if !ok {
				return nil, paramError(path, "an object", v)
			}
			for key, raw := range obj {
				text, err := paramString(raw)
				if err != nil {
					return nil, err
				}
				header.Set(*m.PrefixHeader+key, text)
			}
		case m.Payload:
			payload, payloadValue = m, v
		default:
			body[capitalize(m.Name)] = v
		}
	}

	var missing string
	path := uriLabel.ReplaceAllStringFunc(pathTemplate, func(token string) string {
		name := token[1 : len(token)-1]
		greedy := strings.HasSuffix(name, "+")
		name = strings.TrimSuffix(name, "+")
		value, ok := labels[name]
		if !ok || value == "" {
			if missing == "" {
				missing = capitalize(name)
			}
			return ""
		}
		return escapeLabel(value, greedy)
	})
	if missing != "" {
		return nil, fmt.Errorf("Parameters.%s is required", missing)
	}

	var encoded []byte
	contentType := ""
	switch {
	case payload != nil:
		encoded, contentType, err = c.payloadBody(payload, payloadValue)
		if err != nil {
			return nil, err
		}
	case len(body) > 0 || hasBodyMembers(input):
		if c.protocol == awsapi.ProtocolRESTJSON {
			obj, err := jsonValue(input, nil, body, true, "")
			if err != nil {
				return nil, err
			}
			if encoded, err = json.Marshal(obj); err != nil {
				return nil, err
			}
			contentType = "application/json"
		} else if len(body) > 0 {
			var buf bytes.Buffer
			name := orDefault(input.XMLName, input.Name)
			if err := c.writeXMLElement(&buf, name, orDefault(input.XMLNamespace, c.svc.XMLNamespace), input, nil, body, ""); err != nil {
				return nil, err
			}
			encoded, contentType = buf.Bytes(), "application/xml"
		}
	}

	target := path
	if len(query) > 0 {
		target += "?" + strings.Join(query, "&")
	}
	req, err := http.NewRequestWithContext(ctx, c.shape.Method, target, bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	for name, vals := range header {
		req.Header[name] = vals
	}
	if contentType != "" && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", contentType)
	}
	return req, nil
}

// hasBodyMembers reports whether any input member travels in the body.
func hasBodyMembers(input *awsshapes.Shape) bool {
	for _, m := range input.Members {
		if !m.Label && m.Query == "" && !m.QueryParams && m.Header == "" && m.PrefixHeader == nil && !m.Payload {
			return true
		}
	}
	return false
}

// payloadBody renders an @httpPayload member.
func (c *sdkCall) payloadBody(m *awsshapes.Member, v any) ([]byte, string, error) {
	//exhaustive:ignore
	switch m.Target.Kind {
	case awsshapes.KindBlob:
		b, err := paramBlob(v, true)
		return b, "application/octet-stream", err
	case awsshapes.KindString, awsshapes.KindEnum:
		s, err := paramString(v)
		return []byte(s), "text/plain", err
	case awsshapes.KindDocument:
		b, err := json.Marshal(v)
		return b, "application/json", err
	}
	if c.protocol == awsapi.ProtocolRESTJSON {
		obj, err := jsonValue(m.Target, m, v, true, "."+capitalize(m.Name))
		if err != nil {
			return nil, "", err
		}
		b, err := json.Marshal(obj)
		return b, "application/json", err
	}
	var buf bytes.Buffer
	name := orDefault(m.XMLName, orDefault(m.Target.XMLName, m.Target.Name))
	ns := orDefault(m.XMLNamespace, orDefault(m.Target.XMLNamespace, c.svc.XMLNamespace))
	if err := c.writeXMLElement(&buf, name, ns, m.Target, m, v, "."+capitalize(m.Name)); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), "application/xml", nil
}

// escapeLabel percent-encodes a URI label, keeping '/' in a greedy one.
func escapeLabel(value string, greedy bool) string {
	var out strings.Builder
	for i := 0; i < len(value); i++ {
		ch := value[i]
		switch {
		case 'A' <= ch && ch <= 'Z', 'a' <= ch && ch <= 'z', '0' <= ch && ch <= '9',
			ch == '-', ch == '.', ch == '_', ch == '~':
			out.WriteByte(ch)
		case ch == '/' && greedy:
			out.WriteByte(ch)
		default:
			fmt.Fprintf(&out, "%%%02X", ch)
		}
	}
	return out.String()
}

// writeXMLElement writes one REST-XML element for a structure or scalar.
func (c *sdkCall) writeXMLElement(buf *bytes.Buffer, name, ns string, target *awsshapes.Shape, member *awsshapes.Member, v any, path string) error {
	buf.WriteString("<" + name)
	if ns != "" {
		buf.WriteString(` xmlns="` + xmlEscape(ns) + `"`)
	}
	//exhaustive:ignore
	switch target.Kind {
	case awsshapes.KindStructure, awsshapes.KindUnion:
		members, values, err := structureParams(target, v, path)
		if err != nil {
			return err
		}
		var children bytes.Buffer
		for i, m := range members {
			memberName := orDefault(m.XMLName, m.Name)
			if m.XMLAttribute {
				text, err := scalarText(m.Target, m, values[i], "date-time", path+"."+capitalize(m.Name))
				if err != nil {
					return err
				}
				buf.WriteString(" " + memberName + `="` + xmlEscape(text) + `"`)
				continue
			}
			if err := c.writeXMLMember(&children, memberName, m, values[i], path+"."+capitalize(m.Name)); err != nil {
				return err
			}
		}
		buf.WriteString(">")
		buf.Write(children.Bytes())
	default:
		text, err := scalarText(target, member, v, "date-time", path)
		if err != nil {
			return err
		}
		buf.WriteString(">" + xmlEscape(text))
	}
	buf.WriteString("</" + name + ">")
	return nil
}

// writeXMLMember writes a member, expanding lists and maps.
func (c *sdkCall) writeXMLMember(buf *bytes.Buffer, name string, m *awsshapes.Member, v any, path string) error {
	target := m.Target
	//exhaustive:ignore
	switch target.Kind {
	case awsshapes.KindList:
		arr, ok := v.([]any)
		if !ok {
			return paramError(path, "an array", v)
		}
		if !m.XMLFlattened {
			buf.WriteString("<" + name + ">")
		}
		itemName := name
		if !m.XMLFlattened {
			itemName = orDefault(target.MemberXMLName, "member")
		}
		for i, el := range arr {
			if err := c.writeXMLElement(buf, itemName, "", target.Member, nil, el, path+"["+strconv.Itoa(i)+"]"); err != nil {
				return err
			}
		}
		if !m.XMLFlattened {
			buf.WriteString("</" + name + ">")
		}
		return nil
	case awsshapes.KindMap:
		obj, ok := v.(map[string]any)
		if !ok {
			return paramError(path, "an object", v)
		}
		keys := make([]string, 0, len(obj))
		for k := range obj {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		keyName, valueName := orDefault(target.KeyXMLName, "key"), orDefault(target.ValueXMLName, "value")
		if !m.XMLFlattened {
			buf.WriteString("<" + name + ">")
		}
		for _, k := range keys {
			entry := "entry"
			if m.XMLFlattened {
				entry = name
			}
			buf.WriteString("<" + entry + "><" + keyName + ">" + xmlEscape(k) + "</" + keyName + ">")
			if err := c.writeXMLElement(buf, valueName, "", target.Value, nil, obj[k], path+"."+k); err != nil {
				return err
			}
			buf.WriteString("</" + entry + ">")
		}
		if !m.XMLFlattened {
			buf.WriteString("</" + name + ">")
		}
		return nil
	}
	return c.writeXMLElement(buf, name, m.XMLNamespace, target, m, v, path)
}

func xmlEscape(s string) string {
	var buf bytes.Buffer
	_ = xml.EscapeText(&buf, []byte(s))
	return buf.String()
}
