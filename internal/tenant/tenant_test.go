package tenant

import (
	"strings"
	"testing"
)

func TestParseAndAuthenticate(t *testing.T) {
	key, digest, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(key, KeyPrefix) {
		t.Fatalf("key %q lacks prefix", key)
	}
	reg, err := Parse([]byte(`
tenants:
  - id: yner
    api_keys:
      - sha256: ` + digest + `
    default_sender: { email: no-reply@yner.fr, name: Yner }
    allowed_senders: ["no-reply@yner.fr", "*@notifications.yner.fr"]
`))
	if err != nil {
		t.Fatal(err)
	}

	got, ok := reg.Authenticate(key)
	if !ok || got.ID != "yner" {
		t.Fatalf("Authenticate(valid key) = %v, %v", got, ok)
	}
	for _, bad := range []string{"", key + "x", "kr_nope"} {
		if _, ok := reg.Authenticate(bad); ok {
			t.Errorf("Authenticate(%q) should fail", bad)
		}
	}

	for email, want := range map[string]bool{
		"no-reply@yner.fr":              true,
		"NO-REPLY@yner.fr":              true,
		"billing@notifications.yner.fr": true,
		"ceo@yner.fr":                   false,
		"no-reply@evil.com":             false,
		"x@sub.notifications.yner.fr":   false,
	} {
		if got.CanSendFrom(email) != want {
			t.Errorf("CanSendFrom(%q) = %v, want %v", email, !want, want)
		}
	}
}

func TestParseRejectsInvalidConfig(t *testing.T) {
	digest := HashKey("k")
	tests := map[string]string{
		"bad id":     `tenants: [{id: "Bad ID", api_keys: [{sha256: ` + digest + `}], allowed_senders: ["a@b.fr"]}]`,
		"no key":     `tenants: [{id: a, allowed_senders: ["a@b.fr"]}]`,
		"bad digest": `tenants: [{id: a, api_keys: [{sha256: xyz}], allowed_senders: ["a@b.fr"]}]`,
		"no senders": `tenants: [{id: a, api_keys: [{sha256: ` + digest + `}]}]`,
		"default sender not allowed": `tenants: [{id: a, api_keys: [{sha256: ` + digest + `}],
			allowed_senders: ["a@b.fr"], default_sender: {email: c@d.fr}}]`,
		"shared key": `tenants:
  - {id: a, api_keys: [{sha256: ` + digest + `}], allowed_senders: ["a@b.fr"]}
  - {id: b, api_keys: [{sha256: ` + digest + `}], allowed_senders: ["a@b.fr"]}`,
	}
	for name, yaml := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(yaml)); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}
