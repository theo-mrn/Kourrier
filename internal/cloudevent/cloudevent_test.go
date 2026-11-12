package cloudevent

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func headers(kv ...string) map[string][]string {
	h := map[string][]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		h[kv[i]] = []string{kv[i+1]}
	}
	return h
}

const structured = `{
  "specversion": "1.0",
  "id": "evt-1",
  "source": "/shop/orders",
  "type": "com.example.order.paid",
  "subject": "order-42",
  "time": "2026-11-12T10:00:00Z",
  "datacontenttype": "application/json",
  "tenantid": "acme",
  "sequence": 7,
  "replay": true,
  "nullable": null,
  "data": {"email": "jane@example.com", "id": 9007199254740993, "total": 12.5}
}`

func TestParseStructured(t *testing.T) {
	e, err := Parse(headers("Content-Type", "application/cloudevents+json; charset=utf-8"), []byte(structured))
	if err != nil {
		t.Fatal(err)
	}
	if e.ID != "evt-1" || e.Source != "/shop/orders" || e.Type != "com.example.order.paid" || e.Subject != "order-42" {
		t.Fatalf("unexpected attributes: %+v", e)
	}
	if !e.Time.Equal(time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("time = %v", e.Time)
	}
	want := map[string]string{"tenantid": "acme", "sequence": "7", "replay": "true"}
	if len(e.Extensions) != len(want) {
		t.Fatalf("extensions = %v", e.Extensions)
	}
	for k, v := range want {
		if e.Extensions[k] != v {
			t.Fatalf("extension %s = %q, want %q", k, e.Extensions[k], v)
		}
	}

	data, err := e.DecodeData()
	if err != nil {
		t.Fatal(err)
	}
	m := data.(map[string]any)
	if m["id"] != int64(9007199254740993) {
		t.Fatalf("large integer was not preserved: %#v", m["id"])
	}
	if m["total"] != 12.5 {
		t.Fatalf("float = %#v", m["total"])
	}
}

func TestParseBinary(t *testing.T) {
	// net/http canonicalises header names, NATS does not: both must work.
	for _, prefix := range []string{"Ce-", "ce-"} {
		h := headers(
			prefix+"Specversion", "1.0",
			prefix+"Id", "evt-2",
			prefix+"Source", "urn:example:users",
			prefix+"Type", "com.example.user.created",
			prefix+"Traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
			prefix+"Comexamplenote", "caf%C3%A9%20cr%C3%A8me",
			"Content-Type", "application/json",
		)
		e, err := Parse(h, []byte(`{"email":"a@b.fr"}`))
		if err != nil {
			t.Fatalf("%s: %v", prefix, err)
		}
		if e.ID != "evt-2" || e.DataContentType != "application/json" || string(e.Data) != `{"email":"a@b.fr"}` {
			t.Fatalf("%s: unexpected event %+v", prefix, e)
		}
		if e.Extensions["comexamplenote"] != "café crème" {
			t.Fatalf("%s: percent-decoding failed: %q", prefix, e.Extensions["comexamplenote"])
		}
	}
}

func TestParseBinaryWithoutData(t *testing.T) {
	e, err := Parse(headers("ce-specversion", "1.0", "ce-id", "1", "ce-source", "/s", "ce-type", "t"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if e.Data != nil {
		t.Fatalf("data = %q", e.Data)
	}
	if v, err := e.DecodeData(); v != nil || err != nil {
		t.Fatalf("DecodeData() = %v, %v", v, err)
	}
}

func TestParseErrors(t *testing.T) {
	long := strings.Repeat("a", MaxAttributeBytes+1)
	tooMany := `{"specversion":"1.0","id":"1","source":"/s","type":"t"`
	for i := range MaxExtensions + 1 {
		tooMany += `,"ext` + string(rune('a'+i%26)) + string(rune('a'+i/26)) + `":"x"`
	}
	tooMany += `}`

	tests := []struct {
		name    string
		headers map[string][]string
		body    string
		want    error
	}{
		{"plain json", headers("Content-Type", "application/json"), `{}`, ErrNotCloudEvent},
		{"batch", headers("Content-Type", ContentTypeBatch), `[]`, ErrUnsupported},
		{"bad content type", headers("Content-Type", "a/b; ="), `{}`, ErrMalformed},
		{"not an object", headers("Content-Type", ContentTypeStructured), `[1]`, ErrMalformed},
		{"trailing data", headers("Content-Type", ContentTypeStructured), `{"specversion":"1.0","id":"1","source":"/s","type":"t"} {}`, ErrMalformed},
		{"missing specversion", headers("Content-Type", ContentTypeStructured), `{"id":"1","source":"/s","type":"t"}`, ErrMalformed},
		{"spec 0.3", headers("Content-Type", ContentTypeStructured), `{"specversion":"0.3","id":"1","source":"/s","type":"t"}`, ErrUnsupported},
		{"missing id", headers("Content-Type", ContentTypeStructured), `{"specversion":"1.0","source":"/s","type":"t"}`, ErrMalformed},
		{"empty type", headers("Content-Type", ContentTypeStructured), `{"specversion":"1.0","id":"1","source":"/s","type":""}`, ErrMalformed},
		{"empty subject", headers("Content-Type", ContentTypeStructured), `{"specversion":"1.0","id":"1","source":"/s","type":"t","subject":""}`, ErrMalformed},
		{"bad time", headers("Content-Type", ContentTypeStructured), `{"specversion":"1.0","id":"1","source":"/s","type":"t","time":"yesterday"}`, ErrMalformed},
		{"relative dataschema", headers("Content-Type", ContentTypeStructured), `{"specversion":"1.0","id":"1","source":"/s","type":"t","dataschema":"/x"}`, ErrMalformed},
		{"bad extension name", headers("Content-Type", ContentTypeStructured), `{"specversion":"1.0","id":"1","source":"/s","type":"t","Bad-Name":"x"}`, ErrMalformed},
		{"object extension", headers("Content-Type", ContentTypeStructured), `{"specversion":"1.0","id":"1","source":"/s","type":"t","ext":{}}`, ErrMalformed},
		{"too long", headers("Content-Type", ContentTypeStructured), `{"specversion":"1.0","id":"` + long + `","source":"/s","type":"t"}`, ErrMalformed},
		{"too many extensions", headers("Content-Type", ContentTypeStructured), tooMany, ErrMalformed},
		{"base64 data", headers("Content-Type", ContentTypeStructured), `{"specversion":"1.0","id":"1","source":"/s","type":"t","data_base64":"AA=="}`, ErrUnsupported},
		{"xml data", headers("Content-Type", ContentTypeStructured), `{"specversion":"1.0","id":"1","source":"/s","type":"t","datacontenttype":"application/xml","data":"<a/>"}`, ErrUnsupported},
		{"binary invalid json", headers("ce-specversion", "1.0", "ce-id", "1", "ce-source", "/s", "ce-type", "t", "content-type", "application/json"), `{`, ErrMalformed},
		{"binary text data", headers("ce-specversion", "1.0", "ce-id", "1", "ce-source", "/s", "ce-type", "t", "content-type", "text/plain"), `hello`, ErrUnsupported},
		{"binary bad escape", headers("ce-specversion", "1.0", "ce-id", "%zz", "ce-source", "/s", "ce-type", "t"), ``, ErrMalformed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(tt.headers, []byte(tt.body))
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestAttributes(t *testing.T) {
	e, err := ParseStructured([]byte(structured))
	if err != nil {
		t.Fatal(err)
	}
	a := e.Attributes()
	if a["type"] != "com.example.order.paid" || a["tenantid"] != "acme" || a["subject"] != "order-42" {
		t.Fatalf("attributes = %v", a)
	}
	if _, ok := a["time"].(time.Time); !ok {
		t.Fatalf("time attribute should be a time.Time: %#v", a["time"])
	}
	if _, ok := a["dataschema"]; ok {
		t.Fatal("absent attributes must be omitted")
	}
}

func FuzzParse(f *testing.F) {
	f.Add("application/cloudevents+json", []byte(structured))
	f.Add("application/json", []byte(`{"a":1}`))
	f.Fuzz(func(t *testing.T, ct string, body []byte) {
		e, err := Parse(headers("Content-Type", ct, "ce-specversion", "1.0", "ce-id", "1", "ce-source", "/s", "ce-type", "t"), body)
		if err == nil {
			if e.ID == "" || e.Source == "" || e.Type == "" {
				t.Fatalf("accepted event without required attributes: %+v", e)
			}
			if _, err := e.DecodeData(); err != nil {
				t.Fatalf("accepted event with undecodable data: %v", err)
			}
		}
	})
}

func TestParseNATS(t *testing.T) {
	// No header at all: NATS binding 1.0 structured mode.
	e, err := ParseNATS(nil, []byte(structured))
	if err != nil || e.ID != "evt-1" {
		t.Fatalf("ParseNATS() = %+v, %v", e, err)
	}
	if _, err := ParseNATS(nil, []byte("hello")); !errors.Is(err, ErrMalformed) {
		t.Fatalf("err = %v, want ErrMalformed", err)
	}
	// An explicit non-CloudEvents content type is not guessed.
	if _, err := ParseNATS(headers("Content-Type", "application/json"), []byte(structured)); !errors.Is(err, ErrNotCloudEvent) {
		t.Fatalf("err = %v, want ErrNotCloudEvent", err)
	}
	// Binary mode still works.
	if _, err := ParseNATS(headers("ce-specversion", "1.0", "ce-id", "1", "ce-source", "/s", "ce-type", "t"), nil); err != nil {
		t.Fatal(err)
	}
}

func TestStrictJSON(t *testing.T) {
	deep := strings.Repeat("[", MaxDepth+1) + strings.Repeat("]", MaxDepth+1)
	okDeep := strings.Repeat("[", MaxDepth) + strings.Repeat("]", MaxDepth)
	tests := []struct {
		name, body string
		ok         bool
	}{
		{"valid", `{"a":{"b":[1,{"c":2}],"d":"x"},"e":null}`, true},
		{"same key in sibling objects", `{"a":{"k":1},"b":{"k":2}}`, true},
		{"duplicate top-level key", `{"id":"a","id":"b"}`, false},
		{"duplicate nested key", `{"data":{"email":"a@b.fr","email":"evil@x.fr"}}`, false},
		{"max depth", okDeep, true},
		{"too deep", deep, false},
		{"trailing", `{} {}`, false},
		{"empty", ``, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := checkJSON([]byte(tt.body)); (err == nil) != tt.ok {
				t.Fatalf("checkJSON() = %v, want ok=%v", err, tt.ok)
			}
		})
	}

	// Duplicate attributes and duplicate data keys are rejected in both modes.
	if _, err := ParseStructured([]byte(`{"specversion":"1.0","id":"1","id":"2","source":"/s","type":"t"}`)); !errors.Is(err, ErrMalformed) {
		t.Fatalf("structured duplicate: %v", err)
	}
	h := headers("ce-specversion", "1.0", "ce-id", "1", "ce-source", "/s", "ce-type", "t", "content-type", "application/json")
	if _, err := Parse(h, []byte(`{"to":"a@b.fr","to":"c@d.fr"}`)); !errors.Is(err, ErrMalformed) {
		t.Fatalf("binary duplicate: %v", err)
	}
}

func TestMarshalStructuredRoundTrip(t *testing.T) {
	e, err := ParseStructured([]byte(structured))
	if err != nil {
		t.Fatal(err)
	}
	b, err := e.MarshalStructured()
	if err != nil {
		t.Fatal(err)
	}
	back, err := ParseStructured(b)
	if err != nil {
		t.Fatalf("re-parse %s: %v", b, err)
	}
	if back.ID != e.ID || back.Type != e.Type || !back.Time.Equal(e.Time) || back.Extensions["sequence"] != "7" ||
		back.Subject != e.Subject {
		t.Fatalf("round trip changed the event:\n%+v\n%+v", e, back)
	}
	d1, _ := e.DecodeData()
	d2, _ := back.DecodeData()
	if !reflect.DeepEqual(d1, d2) {
		t.Fatalf("round trip changed the data: %v != %v", d1, d2)
	}
}
