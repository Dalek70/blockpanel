package auth

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

// TOTP per RFC 6238: SHA-1, 6 digits, 30-second step — the profile every
// authenticator app (Microsoft Authenticator, Google Authenticator, Aegis,
// 1Password, ...) supports out of the box.

const (
	totpDigits = 6
	totpStep   = 30 * time.Second
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewTOTPSecret returns a fresh 20-byte secret, base32-encoded for manual
// entry into an authenticator app.
func NewTOTPSecret() (string, error) {
	buf := make([]byte, 20)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return b32.EncodeToString(buf), nil
}

// TOTPURI builds the otpauth:// enrollment URI.
func TOTPURI(secret, account, issuer string) string {
	v := url.Values{}
	v.Set("secret", secret)
	v.Set("issuer", issuer)
	v.Set("algorithm", "SHA1")
	v.Set("digits", "6")
	v.Set("period", "30")
	return fmt.Sprintf("otpauth://totp/%s:%s?%s",
		url.PathEscape(issuer), url.PathEscape(account), v.Encode())
}

func totpCode(secret string, counter int64) (string, error) {
	key, err := b32.DecodeString(strings.ToUpper(strings.ReplaceAll(secret, " ", "")))
	if err != nil {
		return "", fmt.Errorf("bad secret: %w", err)
	}
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(counter))
	mac := hmac.New(sha1.New, key)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	code := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", code%1_000_000), nil
}

// VerifyTOTP checks code against the secret with a ±1 step window.
// lastCounter is the most recent counter already accepted for this user;
// codes at or before it are rejected so a stolen code cannot be replayed.
// On success it returns the counter that matched.
func VerifyTOTP(secret, code string, lastCounter int64) (int64, bool) {
	code = strings.TrimSpace(code)
	if len(code) != totpDigits {
		return 0, false
	}
	now := time.Now().Unix() / int64(totpStep.Seconds())
	for _, c := range []int64{now, now - 1, now + 1} {
		if c <= lastCounter {
			continue
		}
		want, err := totpCode(secret, c)
		if err != nil {
			return 0, false
		}
		if subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 {
			return c, true
		}
	}
	return 0, false
}
