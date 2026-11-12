package cloudevent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// MaxDepth bounds the nesting of JSON payloads. It protects every consumer
// of the data (decoding, expressions, templates) from pathological inputs.
const MaxDepth = 64

// checkJSON verifies that b holds exactly one JSON value, nested at most
// MaxDepth levels, whose objects have no duplicate keys. RFC 8259 leaves the
// meaning of duplicate keys undefined: parsers disagree on which value wins,
// so a producer and Kourrier could read different events from the same
// bytes. Such payloads are rejected rather than guessed.
func checkJSON(b []byte) error {
	type frame struct {
		object  bool
		wantKey bool
		keys    map[string]struct{}
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var (
		stack  []*frame
		values int // complete top-level values
	)
	valueDone := func() {
		if len(stack) == 0 {
			values++
			return
		}
		if top := stack[len(stack)-1]; top.object {
			top.wantKey = true
		}
	}
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if values > 0 {
			return errors.New("trailing data after the JSON value")
		}
		if n := len(stack); n > 0 && stack[n-1].object && stack[n-1].wantKey {
			top := stack[n-1]
			if d, ok := tok.(json.Delim); ok && d == '}' {
				stack = stack[:n-1]
				valueDone()
				continue
			}
			key, _ := tok.(string)
			if _, dup := top.keys[key]; dup {
				return fmt.Errorf("duplicate key %q", key)
			}
			top.keys[key] = struct{}{}
			top.wantKey = false
			continue
		}
		switch tok {
		case json.Delim('{'), json.Delim('['):
			if len(stack) >= MaxDepth {
				return fmt.Errorf("nested deeper than %d levels", MaxDepth)
			}
			obj := tok == json.Delim('{')
			stack = append(stack, &frame{object: obj, wantKey: obj, keys: map[string]struct{}{}})
		case json.Delim(']'):
			stack = stack[:len(stack)-1]
			valueDone()
		default:
			valueDone()
		}
	}
	if values != 1 {
		return errors.New("expected one JSON value")
	}
	return nil
}

// MarshalStructured encodes e in the JSON event format, with extensions in
// their canonical string form. It is used to keep rejected events for replay.
func (e *Event) MarshalStructured() ([]byte, error) {
	m := e.Attributes()
	if e.Data != nil {
		m["data"] = e.Data
	}
	return json.Marshal(m)
}
