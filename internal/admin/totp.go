package admin

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"strings"
	"time"
)

// validateTOTP verifies a 6-digit RFC 6238 code with a +/-1 step window.
// The secret is expected to be base32 (padding optional), as produced by
// authenticator apps and the pquerna/otp library.
func validateTOTP(secret, code string) bool {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != 6 {
		return false
	}
	key, err := decodeTOTPSecret(secret)
	if err != nil || len(key) == 0 {
		return false
	}
	counter := time.Now().Unix() / 30
	for _, offset := range []int64{-1, 0, 1} {
		c := counter + offset
		if c < 0 {
			continue
		}
		candidate := hotp(key, uint64(c))
		if subtle.ConstantTimeCompare([]byte(candidate), []byte(code)) == 1 {
			return true
		}
	}
	return false
}

func decodeTOTPSecret(secret string) ([]byte, error) {
	normalized := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(secret), " ", ""))
	if b, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(normalized); err == nil {
		return b, nil
	}
	return base32.StdEncoding.DecodeString(normalized)
}

func hotp(key []byte, counter uint64) string {
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, counter)
	mac := hmac.New(sha1.New, key)
	mac.Write(buf)
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	value := (uint32(sum[offset]&0x7f) << 24) |
		(uint32(sum[offset+1]&0xff) << 16) |
		(uint32(sum[offset+2]&0xff) << 8) |
		uint32(sum[offset+3]&0xff)
	return fmt.Sprintf("%06d", value%1000000)
}
