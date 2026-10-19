package auth

import (
	"errors"
	"regexp"
	"strings"
)

var (
	ErrPhoneInvalid     = errors.New("invalid phone number")
	ErrPhoneRateLimited = errors.New("phone verification rate limited")
)

var (
	e164Pattern = regexp.MustCompile(`^\+[1-9]\d{6,14}$`)
	cnMobile    = regexp.MustCompile(`^1[3-9]\d{9}$`)
)

var phoneSeparators = strings.NewReplacer(" ", "", "-", "", "(", "", ")", "", "\u00a0", "")

// NormalizePhone accepts E.164 and mainland-China mobile numbers, returning
// the canonical E.164 form used as the account identity.
func NormalizePhone(raw string) (string, error) {
	value := phoneSeparators.Replace(strings.TrimSpace(raw))
	if strings.HasPrefix(value, "00") {
		value = "+" + value[2:]
	}
	if cnMobile.MatchString(value) {
		value = "+86" + value
	}
	if !e164Pattern.MatchString(value) {
		return "", ErrPhoneInvalid
	}
	return value, nil
}
