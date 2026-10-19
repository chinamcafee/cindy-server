package auth

import "testing"

func TestNormalizePhone(t *testing.T) {
	cases := []struct {
		input string
		want  string
		ok    bool
	}{
		{"13800000000", "+8613800000000", true},
		{"+86 138-0000-0000", "+8613800000000", true},
		{"+8613800000000", "+8613800000000", true},
		{"+1 (415) 555-2671", "+14155552671", true},
		{"008613800000000", "+8613800000000", true},
		{"", "", false},
		{"12345", "", false},
		{"not-a-phone", "", false},
	}
	for _, tc := range cases {
		got, err := NormalizePhone(tc.input)
		if tc.ok && (err != nil || got != tc.want) {
			t.Fatalf("NormalizePhone(%q) = %q, %v; want %q", tc.input, got, err, tc.want)
		}
		if !tc.ok && err == nil {
			t.Fatalf("NormalizePhone(%q) expected error", tc.input)
		}
	}
}
