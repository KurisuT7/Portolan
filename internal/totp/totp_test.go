package totp

import (
	"net/url"
	"testing"
	"time"
)

// RFC 6238 Appendix B uses the ASCII seed "12345678901234567890" for SHA-1.
const rfcSecret = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"

func TestCodeMatchesRFC6238Vectors(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		unix int64
		want string
	}{
		{59, "287082"},
		{1111111109, "081804"},
		{1111111111, "050471"},
		{1234567890, "005924"},
		{2000000000, "279037"},
	} {
		got, err := Code(rfcSecret, Step(time.Unix(test.unix, 0)))
		if err != nil {
			t.Fatal(err)
		}
		if got != test.want {
			t.Errorf("Code at %d = %s, want %s", test.unix, got, test.want)
		}
	}
}

func TestVerifyToleratesOneStepAndRejectsReuse(t *testing.T) {
	t.Parallel()
	now := time.Unix(1234567890, 0)
	previous, _ := Code(rfcSecret, Step(now)-1)
	step, ok := Verify(rfcSecret, previous, now, 0)
	if !ok || step != Step(now)-1 {
		t.Fatalf("previous-step code rejected: step=%d ok=%v", step, ok)
	}
	if _, ok := Verify(rfcSecret, previous, now, step); ok {
		t.Fatal("a code was accepted twice")
	}
	stale, _ := Code(rfcSecret, Step(now)-2)
	if _, ok := Verify(rfcSecret, stale, now, 0); ok {
		t.Fatal("a code two steps old was accepted")
	}
	for _, malformed := range []string{"", "12345", "1234567", "abcdef"} {
		if _, ok := Verify(rfcSecret, malformed, now, 0); ok {
			t.Fatalf("malformed code %q was accepted", malformed)
		}
	}
}

func TestNewSecretRoundTripsThroughURI(t *testing.T) {
	t.Parallel()
	secret, err := NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	if len(secret) != 32 {
		t.Fatalf("secret length = %d, want 32 Base32 characters", len(secret))
	}
	parsed, err := url.Parse(URI("Portolan", "panel.example.com", secret))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Scheme != "otpauth" || parsed.Host != "totp" || parsed.Path != "/Portolan:panel.example.com" {
		t.Fatalf("unexpected key URI %s", parsed)
	}
	query := parsed.Query()
	if query.Get("secret") != secret || query.Get("issuer") != "Portolan" || query.Get("digits") != "6" || query.Get("period") != "30" {
		t.Fatalf("unexpected key URI parameters %v", query)
	}
	if _, err := Code(secret, 1); err != nil {
		t.Fatal(err)
	}
}
