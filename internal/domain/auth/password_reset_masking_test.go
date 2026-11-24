package auth

import "testing"

func TestPasswordResetMaskedEmail(t *testing.T) {
	email := "alice@example.com"
	got := passwordResetMaskedEmail(User{Status: 1, Email: &email})
	if got != "al***e@example.com" {
		t.Fatalf("active user masked email = %q, want %q", got, "al***e@example.com")
	}
}

func TestPasswordResetMaskedEmailUsesPlaceholderForUnavailableUsers(t *testing.T) {
	email := "alice@example.com"
	for name, user := range map[string]User{
		"disabled": {Status: 0, Email: &email},
		"no email": {Status: 1},
		"invalid email": {
			Status: 1,
			Email:  func() *string { value := "broken"; return &value }(),
		},
	} {
		t.Run(name, func(t *testing.T) {
			if got := passwordResetMaskedEmail(user); got != "***@***" {
				t.Fatalf("masked email = %q, want %q", got, "***@***")
			}
		})
	}
}
