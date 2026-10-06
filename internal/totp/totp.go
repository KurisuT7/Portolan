// Package totp implements RFC 6238 time-based one-time passwords with the
// parameters authenticator apps use by default: HMAC-SHA1, six digits and a
// 30-second step.
package totp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	digits      = 6
	period      = 30
	secretBytes = 20
)

var encoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewSecret returns a random 160-bit secret in unpadded Base32.
func NewSecret() (string, error) {
	raw := make([]byte, secretBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return encoding.EncodeToString(raw), nil
}

// Step returns the time step that contains t.
func Step(t time.Time) int64 {
	return t.Unix() / period
}

// Code returns the one-time password for a time step.
func Code(secret string, step int64) (string, error) {
	key, err := encoding.DecodeString(strings.ToUpper(secret))
	if err != nil {
		return "", fmt.Errorf("decode TOTP secret: %w", err)
	}
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(step))
	mac := hmac.New(sha1.New, key)
	mac.Write(counter[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	value := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
	return fmt.Sprintf("%0*d", digits, value%1_000_000), nil
}

// Verify accepts a code from the step that contains now or one of its two
// neighbours, which tolerates clock drift of up to 30 seconds. Steps at or
// before lastUsed are rejected so a code works only once. It returns the
// matching step for the caller to record.
func Verify(secret, code string, now time.Time, lastUsed int64) (int64, bool) {
	code = strings.TrimSpace(code)
	if len(code) != digits {
		return 0, false
	}
	current := Step(now)
	for _, step := range []int64{current - 1, current, current + 1} {
		if step <= lastUsed {
			continue
		}
		expected, err := Code(secret, step)
		if err != nil {
			return 0, false
		}
		if subtle.ConstantTimeCompare([]byte(expected), []byte(code)) == 1 {
			return step, true
		}
	}
	return 0, false
}

// URI returns the otpauth key URI that authenticator apps import from a QR code.
func URI(issuer, account, secret string) string {
	query := url.Values{}
	query.Set("secret", secret)
	query.Set("issuer", issuer)
	query.Set("algorithm", "SHA1")
	query.Set("digits", fmt.Sprint(digits))
	query.Set("period", fmt.Sprint(period))
	return "otpauth://totp/" + url.PathEscape(issuer+":"+account) + "?" + query.Encode()
}
