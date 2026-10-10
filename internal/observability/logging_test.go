package observability

import "testing"

func TestMaskEmail(t *testing.T) {
	for in, want := range map[string]string{
		"jane.doe@example.com": "j***@example.com",
		"a@b.fr":               "a***@b.fr",
		"invalid":              "***",
		"@nolocal.fr":          "***",
	} {
		if got := MaskEmail(in); got != want {
			t.Errorf("MaskEmail(%q) = %q, want %q", in, got, want)
		}
	}
}
