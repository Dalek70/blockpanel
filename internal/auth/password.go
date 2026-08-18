// Package auth implements password hashing (PBKDF2-HMAC-SHA256) and TOTP
// two-factor codes using only the standard library.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"hash"
	"strconv"
	"strings"
	"unicode"
)

// OWASP-recommended work factor for PBKDF2-HMAC-SHA256 (2023 guidance: 600k).
const pbkdf2Iterations = 600_000

// pbkdf2Key implements RFC 2898. Hand-rolled to keep the module free of
// external dependencies and independent of Go version.
func pbkdf2Key(password, salt []byte, iter, keyLen int, h func() hash.Hash) []byte {
	prf := hmac.New(h, password)
	hashLen := prf.Size()
	numBlocks := (keyLen + hashLen - 1) / hashLen

	var buf [4]byte
	dk := make([]byte, 0, numBlocks*hashLen)
	U := make([]byte, hashLen)
	for block := 1; block <= numBlocks; block++ {
		prf.Reset()
		prf.Write(salt)
		buf[0] = byte(block >> 24)
		buf[1] = byte(block >> 16)
		buf[2] = byte(block >> 8)
		buf[3] = byte(block)
		prf.Write(buf[:4])
		dk = prf.Sum(dk)
		T := dk[len(dk)-hashLen:]
		copy(U, T)
		for n := 2; n <= iter; n++ {
			prf.Reset()
			prf.Write(U)
			U = U[:0]
			U = prf.Sum(U)
			U = U[:hashLen]
			for x := range T {
				T[x] ^= U[x]
			}
		}
	}
	return dk[:keyLen]
}

// HashPassword returns "pbkdf2$sha256$<iter>$<salt-b64>$<hash-b64>".
func HashPassword(password string) (string, error) {
	if err := CheckPasswordPolicy(password); err != nil {
		return "", err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := pbkdf2Key([]byte(password), salt, pbkdf2Iterations, 32, sha256.New)
	return fmt.Sprintf("pbkdf2$sha256$%d$%s$%s",
		pbkdf2Iterations,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword reports whether password matches the encoded hash.
func VerifyPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 5 || parts[0] != "pbkdf2" || parts[1] != "sha256" {
		return false
	}
	iter, err := strconv.Atoi(parts[2])
	if err != nil || iter < 1 || iter > 10_000_000 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	got := pbkdf2Key([]byte(password), salt, iter, len(want), sha256.New)
	return subtle.ConstantTimeCompare(got, want) == 1
}

// CheckPasswordPolicy enforces a minimal sane policy: 10+ chars, not all one
// character class. Length beats composition rules (NIST 800-63B).
func CheckPasswordPolicy(password string) error {
	if len(password) < 10 {
		return errors.New("password must be at least 10 characters")
	}
	if len(password) > 256 {
		return errors.New("password too long (max 256)")
	}
	var letters, other bool
	for _, r := range password {
		if unicode.IsLetter(r) {
			letters = true
		} else {
			other = true
		}
	}
	if !letters || !other {
		return errors.New("password must mix letters with digits or symbols")
	}
	return nil
}
