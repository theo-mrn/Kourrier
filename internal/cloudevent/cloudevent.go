// Package cloudevent implements the subset of the CloudEvents 1.0
// specification that Kourrier consumes (ADR 012): JSON events in structured
// or binary content mode, as defined by the HTTP and NATS protocol bindings.
//
// The parser is deliberately strict and bounded. Events come from other
// systems, possibly compromised ones, so every attribute is validated and
// size-limited before anything else looks at it. Batch mode and non-JSON
// payloads are rejected with ErrUnsupported.
package cloudevent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// SpecVersion is the only CloudEvents version accepted.
const SpecVersion = "1.0"

// Content types defined by the CloudEvents JSON event format.
const (
	ContentTypeStructured = "application/cloudevents+json"
	ContentTypeBatch      = "application/cloudevents-batch+json"
)

// Limits applied to every event. They bound memory use and the size of the
// identifiers derived from attributes (idempotency keys, logs).
const (
	MaxAttributeBytes = 2048
	MaxExtensions     = 32
)

var (
	// ErrNotCloudEvent means the message carries no CloudEvents attributes.
	ErrNotCloudEvent = errors.New("not a cloudevent")
	// ErrMalformed means the event violates the specification.
	ErrMalformed = errors.New("malformed cloudevent")
	// ErrUnsupported means the event is valid but uses a feature Kourrier does
	// not accept: batch mode, binary or non-JSON data, another spec version.
	ErrUnsupported = errors.New("unsupported cloudevent")
)

// extensionName is the format mandated by the specification for attribute
// names (lower-case letters and digits). The specification only recommends
// 20 characters at most, so longer names are accepted up to a safety cap.
var extensionName = regexp.MustCompile(`^[a-z0-9]{1,64}$`)

// Event is a validated CloudEvent whose data, if any, is JSON.
type Event struct {
	ID              string
	Source          string
	SpecVersion     string
	Type            string
	Subject         string
	Time            time.Time // zero when absent
	DataContentType string
	DataSchema      string
	// Extensions holds extension attributes in their canonical string form,
	// whatever the content mode they were received in.
	Extensions map[string]string
	// Data is the JSON payload, nil when the event has no data.
	Data json.RawMessage
}

// Parse decodes a message received through a protocol binding that carries
// headers (HTTP, NATS). Header names are matched case-insensitively. The
// content mode is chosen from the Content-Type header: structured for
// application/cloudevents+json, binary when ce-* headers are present.
func Parse(headers map[string][]string, body []byte) (*Event, error) {
	h := make(map[string]string, len(headers))
	for k, v := range headers {
		if len(v) > 0 {
			h[strings.ToLower(k)] = v[0]
		}
	}
	mediaType := ""
	if ct := h["content-type"]; ct != "" {
		mt, _, err := mime.ParseMediaType(ct)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid content-type %q", ErrMalformed, ct)
		}
		mediaType = mt
	}

	switch {
	case mediaType == ContentTypeBatch:
		return nil, fmt.Errorf("%w: batch content mode", ErrUnsupported)
	case mediaType == ContentTypeStructured:
		return ParseStructured(body)
	case h["ce-specversion"] != "":
		return parseBinary(h, h["content-type"], body)
	default:
		return nil, ErrNotCloudEvent
	}
}

// ParseNATS decodes a NATS message. Version 1.0 of the NATS protocol binding
// predates NATS headers and only defines the structured mode with no content
// type: a message without ce-* headers nor Content-Type is therefore read as
// a structured JSON event. Outbox relays commonly publish events this way.
func ParseNATS(headers map[string][]string, body []byte) (*Event, error) {
	e, err := Parse(headers, body)
	if !errors.Is(err, ErrNotCloudEvent) {
		return e, err
	}
	for k := range headers {
		if strings.EqualFold(k, "content-type") {
			return nil, err
		}
	}
	return ParseStructured(body)
}

// ParseStructured decodes an event in the JSON event format.
func ParseStructured(body []byte) (*Event, error) {
	if err := checkJSON(body); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrMalformed, err)
	}
	var raw map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(&raw); err != nil || raw == nil {
		return nil, fmt.Errorf("%w: body must be a JSON object", ErrMalformed)
	}
	if dec.More() {
		return nil, fmt.Errorf("%w: trailing data after the JSON object", ErrMalformed)
	}

	attrs := make(map[string]string, len(raw))
	var data json.RawMessage
	for name, value := range raw {
		switch name {
		case "data":
			if !isNull(value) {
				data = value
			}
			continue
		case "data_base64":
			return nil, fmt.Errorf("%w: data_base64 (binary data)", ErrUnsupported)
		}
		s, present, err := attributeString(value)
		if err != nil {
			return nil, fmt.Errorf("%w: attribute %q: %w", ErrMalformed, name, err)
		}
		if present {
			attrs[name] = s
		}
	}
	return build(attrs, data, true)
}

func parseBinary(h map[string]string, contentType string, body []byte) (*Event, error) {
	attrs := make(map[string]string)
	for k, v := range h {
		name, ok := strings.CutPrefix(k, "ce-")
		if !ok {
			continue
		}
		// Header values are percent-encoded by the HTTP and NATS bindings.
		decoded, err := url.PathUnescape(v)
		if err != nil {
			return nil, fmt.Errorf("%w: header %q is not correctly percent-encoded", ErrMalformed, k)
		}
		attrs[name] = decoded
	}
	if contentType != "" {
		attrs["datacontenttype"] = contentType
	}
	var data json.RawMessage
	if len(bytes.TrimSpace(body)) > 0 {
		data = body
	}
	return build(attrs, data, false)
}

// build validates the attributes and assembles the event.
func build(attrs map[string]string, data json.RawMessage, structured bool) (*Event, error) {
	e := &Event{}
	required := map[string]*string{"id": &e.ID, "source": &e.Source, "specversion": &e.SpecVersion, "type": &e.Type}
	optional := map[string]*string{"subject": &e.Subject, "datacontenttype": &e.DataContentType, "dataschema": &e.DataSchema}

	var timeAttr string
	for name, value := range attrs {
		if len(value) > MaxAttributeBytes {
			return nil, fmt.Errorf("%w: attribute %q exceeds %d bytes", ErrMalformed, name, MaxAttributeBytes)
		}
		switch {
		case required[name] != nil:
			*required[name] = value
		case optional[name] != nil:
			*optional[name] = value
		case name == "time":
			timeAttr = value
		default:
			if !extensionName.MatchString(name) {
				return nil, fmt.Errorf("%w: invalid attribute name %q", ErrMalformed, name)
			}
			if e.Extensions == nil {
				e.Extensions = make(map[string]string)
			}
			e.Extensions[name] = value
		}
	}

	if e.SpecVersion != SpecVersion {
		if e.SpecVersion == "" {
			return nil, fmt.Errorf("%w: specversion is required", ErrMalformed)
		}
		return nil, fmt.Errorf("%w: specversion %q (only %s is accepted)", ErrUnsupported, e.SpecVersion, SpecVersion)
	}
	for _, name := range []string{"id", "source", "type"} {
		if *required[name] == "" {
			return nil, fmt.Errorf("%w: %s is required", ErrMalformed, name)
		}
	}
	if _, err := url.Parse(e.Source); err != nil {
		return nil, fmt.Errorf("%w: source must be a URI-reference", ErrMalformed)
	}
	if _, present := attrs["subject"]; present && e.Subject == "" {
		return nil, fmt.Errorf("%w: subject must not be empty", ErrMalformed)
	}
	if e.DataSchema != "" {
		if u, err := url.Parse(e.DataSchema); err != nil || !u.IsAbs() {
			return nil, fmt.Errorf("%w: dataschema must be an absolute URI", ErrMalformed)
		}
	}
	if timeAttr != "" {
		t, err := time.Parse(time.RFC3339Nano, timeAttr)
		if err != nil {
			return nil, fmt.Errorf("%w: time must be an RFC 3339 timestamp", ErrMalformed)
		}
		e.Time = t
	}
	if len(e.Extensions) > MaxExtensions {
		return nil, fmt.Errorf("%w: more than %d extension attributes", ErrMalformed, MaxExtensions)
	}

	if data != nil {
		if !isJSONMediaType(e.DataContentType) {
			return nil, fmt.Errorf("%w: datacontenttype %q (only JSON data is accepted)", ErrUnsupported, e.DataContentType)
		}
		// In structured mode the whole body was already checked.
		if !structured {
			if err := checkJSON(data); err != nil {
				return nil, fmt.Errorf("%w: data: %w", ErrMalformed, err)
			}
		}
		e.Data = data
	}
	return e, nil
}

// attributeString converts a JSON attribute value to its canonical string
// form. JSON null means the attribute is absent.
func attributeString(v json.RawMessage) (s string, present bool, err error) {
	var x any
	if err := json.Unmarshal(v, &x); err != nil {
		return "", false, err
	}
	switch val := x.(type) {
	case nil:
		return "", false, nil
	case string:
		return val, true, nil
	case bool:
		if val {
			return "true", true, nil
		}
		return "false", true, nil
	case float64:
		// Keep the original literal: it is the canonical integer form.
		return string(bytes.TrimSpace(v)), true, nil
	default:
		return "", false, errors.New("must be a string, number or boolean")
	}
}

// isJSONMediaType reports whether ct denotes JSON data. An absent content
// type is assumed to be JSON, as the JSON event format specifies.
func isJSONMediaType(ct string) bool {
	if ct == "" {
		return true
	}
	mt, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return false
	}
	return mt == "application/json" || mt == "text/json" || strings.HasSuffix(mt, "+json")
}

func isNull(v json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(v), []byte("null"))
}

// DecodeData decodes the JSON payload into Go values suitable for expression
// evaluation and template rendering. Integers are kept as int64 when they fit,
// so that identifiers above 2^53 are not silently rounded; other numbers
// become float64. It returns nil when the event has no data.
func (e *Event) DecodeData() (any, error) {
	if e.Data == nil {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(e.Data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("%w: data: %w", ErrMalformed, err)
	}
	return convertNumbers(v), nil
}

func convertNumbers(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			x[k] = convertNumbers(val)
		}
		return x
	case []any:
		for i, val := range x {
			x[i] = convertNumbers(val)
		}
		return x
	case json.Number:
		if i, err := x.Int64(); err == nil {
			return i
		}
		f, _ := x.Float64()
		return f
	default:
		return v
	}
}

// Attributes returns the context attributes as a map, including extensions.
// Absent optional attributes are omitted. It is the "event" variable exposed
// to routing expressions.
func (e *Event) Attributes() map[string]any {
	m := make(map[string]any, 8+len(e.Extensions))
	for k, v := range e.Extensions {
		m[k] = v
	}
	m["id"], m["source"], m["specversion"], m["type"] = e.ID, e.Source, e.SpecVersion, e.Type
	if e.Subject != "" {
		m["subject"] = e.Subject
	}
	if !e.Time.IsZero() {
		m["time"] = e.Time
	}
	if e.DataContentType != "" {
		m["datacontenttype"] = e.DataContentType
	}
	if e.DataSchema != "" {
		m["dataschema"] = e.DataSchema
	}
	return m
}
