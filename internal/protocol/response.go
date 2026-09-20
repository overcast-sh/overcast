package protocol

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"strconv"
)

const defaultAWSJSONContentType = "application/x-amz-json-1.0"

// WriteXML serialises v as XML and writes it with the correct Content-Type
// and request ID headers. Services call this for successful responses.
func WriteXML(w http.ResponseWriter, r *http.Request, status int, v any) {
	WriteXMLNS(w, r, status, "", v)
}

// WriteXMLNS is WriteXML for a REST-XML service whose Smithy model declares a
// service-level xmlNamespace: it stamps namespace on the response's root
// element as the default xmlns, which is where AWS puts it. Nested elements
// inherit that default and never repeat the attribute.
//
// An empty namespace writes exactly the bytes WriteXML has always written.
func WriteXMLNS(w http.ResponseWriter, r *http.Request, status int, namespace string, v any) {
	reqID := RequestIDFromContext(r.Context())

	body, err := xml.Marshal(v)
	if err == nil && namespace != "" {
		body, err = stampXMLNamespace(body, namespace)
	}
	if err != nil {
		if namespace != "" {
			WriteRESTXMLError(w, r, namespace, ErrInternalError)
		} else {
			WriteXMLError(w, r, ErrInternalError)
		}
		return
	}

	// Drain the request body so the HTTP/1.1 connection can be reused.
	if r.Body != nil {
		io.Copy(io.Discard, r.Body) //nolint:errcheck
		r.Body.Close()              //nolint:errcheck
	}

	full := append([]byte(xml.Header), body...)
	w.Header().Set("Content-Type", "application/xml")
	w.Header().Set("Content-Length", strconv.Itoa(len(full)))
	w.Header().Set("x-amz-request-id", reqID)
	w.WriteHeader(status)
	w.Write(full) //nolint:errcheck
}

// stampXMLNamespace re-emits doc with namespace declared as the default xmlns
// on its root element, leaving every other token as encoding/xml wrote it.
//
// The namespace belongs to the response, not to the Go type: the same struct
// is a root in one operation (CloudFront's GetDistributionConfig) and a
// nested member in another (GetDistribution), and only the root carries the
// attribute. A field on the type could not tell those apart — and a helper
// that stamps whatever it is handed cannot miss a response the way adding a
// field to each root struct by hand can. Round-tripping at token level is
// safe because the same encoder does the escaping on both passes.
func stampXMLNamespace(doc []byte, namespace string) ([]byte, error) {
	dec := xml.NewDecoder(bytes.NewReader(doc))
	var buf bytes.Buffer
	enc := xml.NewEncoder(&buf)
	stamped := false
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		// A namespace the input already declares would be resolved onto every
		// element's Name.Space and re-declared on each one by the encoder.
		// xml.Marshal never emits one, so this only ever clears an empty
		// field — but it keeps "declared once, on the root" true of any input.
		switch t := tok.(type) {
		case xml.StartElement:
			t.Name.Space = ""
			if !stamped {
				stamped = true
				attrs := make([]xml.Attr, 0, len(t.Attr)+1)
				attrs = append(attrs, xml.Attr{Name: xml.Name{Local: "xmlns"}, Value: namespace})
				t.Attr = append(attrs, t.Attr...)
			}
			tok = t
		case xml.EndElement:
			t.Name.Space = ""
			tok = t
		}
		if err := enc.EncodeToken(tok); err != nil {
			return nil, err
		}
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// WriteJSON serialises v as JSON and writes it with the correct Content-Type
// and request ID headers. Services call this for successful responses.
func WriteJSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	WriteAWSJSON(w, r, status, v, defaultAWSJSONContentType)
}

// WriteAWSJSON serialises v as JSON and writes it with the provided AWS JSON
// content type and request ID headers.
//
// Pass an explicit content type per service protocol, for example:
//   - application/x-amz-json-1.0
//   - application/x-amz-json-1.1
//
// If contentType is empty, application/x-amz-json-1.0 is used.
func WriteAWSJSON(w http.ResponseWriter, r *http.Request, status int, v any, contentType string) {
	reqID := RequestIDFromContext(r.Context())

	body, err := json.Marshal(v)
	if err != nil {
		WriteJSONError(w, r, ErrInternalError)
		return
	}

	// Drain the request body so the HTTP/1.1 connection can be reused.
	if r.Body != nil {
		io.Copy(io.Discard, r.Body) //nolint:errcheck
		r.Body.Close()              //nolint:errcheck
	}

	if contentType == "" {
		contentType = defaultAWSJSONContentType
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Header().Set("x-amzn-requestid", reqID)
	w.WriteHeader(status)
	w.Write(body) //nolint:errcheck
}

// WriteRESTJSON serialises v as the successful response of a REST-JSON
// service, whose content type is plain application/json rather than one of the
// x-amz-json target protocols.
//
// It exists so a REST-JSON handler has somewhere to go other than encoding
// straight onto the ResponseWriter. That shortcut is what left Lambda's whole
// success surface without the x-amzn-RequestId header AWS answers every
// operation with: the header is the write helpers' job (see
// middleware.RequestID), so a handler that bypasses them silently opts out of
// it, and no test noticed because every request-ID assertion in the suite was
// on an error response, which does go through a helper.
func WriteRESTJSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	WriteAWSJSON(w, r, status, v, "application/json")
}

// WriteEmpty writes a response with no body and the standard request ID header.
// Used for operations like DeleteObject which return 204 or an empty 200.
func WriteEmpty(w http.ResponseWriter, r *http.Request, status int) {
	if r.Body != nil {
		io.Copy(io.Discard, r.Body) //nolint:errcheck
		r.Body.Close()              //nolint:errcheck
	}
	reqID := RequestIDFromContext(r.Context())
	w.Header().Set("x-amz-request-id", reqID)
	w.Header().Set("Content-Length", "0")
	w.WriteHeader(status)
}

// ResponseMetadata is embedded in AWS Query-protocol XML responses.
// It carries the request ID that SDKs surface as response.ResultMetadata.
type ResponseMetadata struct {
	RequestID string `xml:"RequestId"`
}

// QueryResponseMetadata returns a ResponseMetadata populated from the request context.
func QueryResponseMetadata(r *http.Request) ResponseMetadata {
	return ResponseMetadata{RequestID: RequestIDFromContext(r.Context())}
}

// WriteQueryXML serialises v as XML with text/xml content type.
// Used by Query-protocol services (SNS, STS, IAM). The request ID is set as both a
// response header and should be embedded in the response struct's ResponseMetadata.
// The request body is drained so the HTTP/1.1 connection can be reused by the SDK client.
func WriteQueryXML(w http.ResponseWriter, r *http.Request, status int, v any) {
	reqID := RequestIDFromContext(r.Context())

	body, err := xml.Marshal(v)
	if err != nil {
		WriteQueryXMLError(w, r, ErrInternalError)
		return
	}

	// Drain the request body so the HTTP/1.1 connection can be reused by the
	// SDK client. Without this, the client logs "failed to close HTTP response
	// body, this may affect connection reuse".
	if r.Body != nil {
		io.Copy(io.Discard, r.Body) //nolint:errcheck
		r.Body.Close()              //nolint:errcheck
	}

	full := append([]byte(xml.Header), body...)
	w.Header().Set("Content-Type", "text/xml")
	w.Header().Set("Content-Length", strconv.Itoa(len(full)))
	w.Header().Set("x-amzn-requestid", reqID)
	w.WriteHeader(status)
	w.Write(full) //nolint:errcheck
}
