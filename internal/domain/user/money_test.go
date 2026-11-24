package user

import (
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestNormalizeBalanceKeepsDecimalPrecision(t *testing.T) {
	got, err := normalizeBalance("99999999999999.9999")
	if err != nil {
		t.Fatalf("normalizeBalance returned error: %v", err)
	}
	want := "99999999999999.9999"
	if got != want {
		t.Fatalf("normalizeBalance() = %q, want %q", got, want)
	}
	if _, err := normalizeBalance("100000000000000.0000"); err == nil {
		t.Fatal("normalizeBalance accepted a value outside decimal(18,4)")
	}
}

func TestNormalizeBalanceRejectsNonCanonicalAndExtremeInputs(t *testing.T) {
	for _, input := range []string{"1/2", "+1", ".5", "1.", "1e1001", "1e-1001"} {
		if _, err := normalizeBalance(input); err == nil {
			t.Fatalf("normalizeBalance accepted invalid input %q", input)
		}
	}
}

func TestDefaultHomePathUsesConsoleWorkspace(t *testing.T) {
	got := defaultHomePath("")
	want := "/console/workspace"
	if got != want {
		t.Fatalf("defaultHomePath() = %q, want %q", got, want)
	}
}

func TestNormalizeSaveUserValidatesDatabaseAndSecurityBoundaries(t *testing.T) {
	service := &Service{}
	status := 1
	points := int64(10)
	request := SaveUserRequest{
		Balance:  "10.0000",
		Email:    "USER@example.com",
		HomePath: "/console/workspace",
		Mobile:   "+8613800000000",
		Password: "  secure password  ",
		Points:   &points,
		RealName: strings.Repeat("名", 64),
		RoleIDs:  []int64{1, 1},
		Status:   &status,
		Username: "valid_user",
	}
	normalized, hash, _, _, err := service.normalizeSaveUser(request, true)
	if err != nil {
		t.Fatalf("normalizeSaveUser() error = %v", err)
	}
	if normalized.Email != "user@example.com" || len(normalized.RoleIDs) != 1 {
		t.Fatalf("unexpected normalized user: %#v", normalized)
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(request.Password)) != nil {
		t.Fatal("password whitespace was unexpectedly changed")
	}

	invalid := []SaveUserRequest{
		{Username: "valid_user", RealName: "Name", Email: "not-an-email", HomePath: "/console", Password: "123456", RoleIDs: []int64{1}},
		{Username: "valid_user", RealName: "Name", Mobile: "mobile<script>", HomePath: "/console", Password: "123456", RoleIDs: []int64{1}},
		{Username: "valid_user", RealName: "Name", HomePath: "https://evil.example", Password: "123456", RoleIDs: []int64{1}},
		{Username: "valid_user", RealName: "Name", HomePath: "/console", Password: "123456", RoleIDs: []int64{0, 1}},
	}
	for index, item := range invalid {
		item.Status = &status
		if _, _, _, _, err := service.normalizeSaveUser(item, true); err == nil {
			t.Fatalf("invalid user request %d was accepted", index)
		}
	}
}
