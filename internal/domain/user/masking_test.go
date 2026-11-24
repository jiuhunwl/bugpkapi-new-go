package user

import "testing"

func TestToUserInfoMasksContactFields(t *testing.T) {
	got := toUserInfo(UserRecord{
		ID:       1,
		Username: "alice",
		Email:    "alice@example.com",
		Mobile:   "13800138000",
	})
	if got.Email != "al***e@example.com" {
		t.Fatalf("email = %q", got.Email)
	}
	if got.Mobile != "138****8000" {
		t.Fatalf("mobile = %q", got.Mobile)
	}
}

func TestMaskSystemUserMasksContactFields(t *testing.T) {
	got := maskSystemUser(SystemUser{
		Email:  "alice@example.com",
		Mobile: "13800138000",
	})
	if got.Email != "al***e@example.com" {
		t.Fatalf("email = %q", got.Email)
	}
	if got.Mobile != "138****8000" {
		t.Fatalf("mobile = %q", got.Mobile)
	}
}
