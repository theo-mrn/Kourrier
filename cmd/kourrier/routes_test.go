package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The examples are part of the documentation: they must stay valid.
func TestRoutesCommandsOnExamples(t *testing.T) {
	var out bytes.Buffer
	if err := run([]string{"routes", "check", "-tenants", "../../dev/tenants.yaml", "../../examples/routes.yaml"}, &out); err != nil {
		t.Fatalf("check: %v\n%s", err, out.String())
	}
	out.Reset()
	err := run([]string{"routes", "test", "-tenants", "../../dev/tenants.yaml", "-tenant", "demo",
		"-event", "../../examples/events/user-signed-up.json", "../../examples/routes.yaml"}, &out)
	if err != nil {
		t.Fatalf("test: %v\n%s", err, out.String())
	}
	for _, want := range []string{`"route": "welcome"`, `"email": "jane@example.com"`, `"locale": "fr"`, `"email": "no-reply@demo.kourrier.dev"`} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output does not contain %s:\n%s", want, out.String())
		}
	}
}

func TestRoutesTestReportsFailures(t *testing.T) {
	dir := t.TempDir()
	event := filepath.Join(dir, "event.json")
	if err := os.WriteFile(event, []byte(`{"specversion":"1.0","id":"1","source":"/s","type":"dev.kourrier.demo.user.signed_up",
		"data":{"email":"not-an-address","first_name":"Jane","email_verified":true}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := run([]string{"routes", "test", "-tenants", "../../dev/tenants.yaml", "-tenant", "demo", "-event", event, "../../examples/routes.yaml"}, &out)
	if err == nil || !strings.Contains(out.String(), "not a valid e-mail address") {
		t.Fatalf("expected a validation failure, got %v:\n%s", err, out.String())
	}
	if err := run([]string{"routes", "test", "-tenants", "../../dev/tenants.yaml", "-tenant", "ghost", "-event", event, "../../examples/routes.yaml"}, &out); err == nil {
		t.Fatal("unknown tenant must fail")
	}
}
