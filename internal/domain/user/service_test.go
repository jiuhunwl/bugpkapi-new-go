package user

import (
	"reflect"
	"strings"
	"testing"
	"time"

	apperrors "bugpk-api/internal/pkg/errors"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"golang.org/x/crypto/bcrypt"
)

func TestParseAdminWorkbenchSections(t *testing.T) {
	sections, err := ParseAdminWorkbenchSections("hotApis,overview,hotApis")
	if err != nil {
		t.Fatalf("ParseAdminWorkbenchSections() error = %v", err)
	}
	want := []AdminWorkbenchSection{AdminWorkbenchSectionHotAPIs, AdminWorkbenchSectionOverview}
	if !reflect.DeepEqual(sections, want) {
		t.Fatalf("sections = %#v, want %#v", sections, want)
	}

	if _, err := ParseAdminWorkbenchSections("overview,unknown"); err == nil {
		t.Fatal("ParseAdminWorkbenchSections() accepted an unknown section")
	}

	all, err := ParseAdminWorkbenchSections("")
	if err != nil {
		t.Fatalf("ParseAdminWorkbenchSections(empty) error = %v", err)
	}
	if len(all) != len(allAdminWorkbenchSections) {
		t.Fatalf("default sections = %d, want %d", len(all), len(allAdminWorkbenchSections))
	}
}

func TestNormalizeSaveUserIgnoresPasswordOnUpdateWithoutChangePassword(t *testing.T) {
	service := &Service{}
	req := validSaveUserRequest()
	req.Password = "new-secret"
	req.ChangePassword = false

	_, passwordHash, _, _, err := service.normalizeSaveUser(req, false)
	if err != nil {
		t.Fatalf("normalizeSaveUser() error = %v", err)
	}
	if passwordHash != "" {
		t.Fatalf("expected empty password hash, got %q", passwordHash)
	}
}

func TestNormalizeSaveUserHashesPasswordOnUpdateWithChangePassword(t *testing.T) {
	service := &Service{}
	req := validSaveUserRequest()
	req.Password = "new-secret"
	req.ChangePassword = true

	_, passwordHash, _, _, err := service.normalizeSaveUser(req, false)
	if err != nil {
		t.Fatalf("normalizeSaveUser() error = %v", err)
	}
	if passwordHash == "" {
		t.Fatal("expected password hash")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte("new-secret")); err != nil {
		t.Fatalf("password hash does not match: %v", err)
	}
}

func TestWorkbenchOpenTrafficConditionExcludesAdminTestLogs(t *testing.T) {
	got := workbenchOpenTrafficCondition("l")
	if !strings.Contains(got, "l.charge_type") {
		t.Fatalf("expected aliased charge_type predicate, got %q", got)
	}
	if !strings.Contains(got, "<> 'ADMIN'") {
		t.Fatalf("expected ADMIN management-test logs to be excluded, got %q", got)
	}
	if strings.Contains(strings.ToLower(got), "coalesce(") {
		t.Fatalf("open traffic predicate should keep charge_type indexable, got %q", got)
	}
	if strings.ContainsAny(got, ";?") {
		t.Fatalf("workbench open traffic predicate should stay a static SQL fragment, got %q", got)
	}

	plain := workbenchOpenTrafficCondition("")
	if strings.Contains(plain, "l.charge_type") || !strings.Contains(plain, "charge_type") {
		t.Fatalf("unexpected unaliased predicate: %q", plain)
	}
}

func TestNormalizeSaveUserAcceptsFutureTemporaryBan(t *testing.T) {
	now := time.Date(2026, time.August, 9, 12, 0, 0, 0, time.Local)
	service := &Service{now: func() time.Time { return now }}
	req := validSaveUserRequest()
	status := 0
	permanent := 0
	req.Status = &status
	req.BanPermanent = &permanent
	req.BanUntil = "2026-08-10 08:30:00"
	req.BanReason = "Repeated abusive traffic"

	normalized, _, gotStatus, _, err := service.normalizeSaveUser(req, false)
	if err != nil {
		t.Fatalf("normalizeSaveUser() error = %v", err)
	}
	if gotStatus != 0 || normalized.BanPermanent == nil || *normalized.BanPermanent != 0 {
		t.Fatalf("temporary ban normalized incorrectly: status=%d request=%+v", gotStatus, normalized)
	}
	if normalized.BanUntil != "2026-08-10 08:30:00" || normalized.BanReason != "Repeated abusive traffic" {
		t.Fatalf("temporary ban values = until %q reason %q", normalized.BanUntil, normalized.BanReason)
	}
}

func TestNormalizeSaveUserRejectsExpiredTemporaryBan(t *testing.T) {
	now := time.Date(2026, time.August, 9, 12, 0, 0, 0, time.Local)
	service := &Service{now: func() time.Time { return now }}
	req := validSaveUserRequest()
	status := 0
	permanent := 0
	req.Status = &status
	req.BanPermanent = &permanent
	req.BanUntil = "2026-08-09 11:59:59"
	req.BanReason = "Repeated abusive traffic"

	_, _, _, _, err := service.normalizeSaveUser(req, false)
	business, ok := apperrors.AsBusinessError(err)
	if !ok || business.StatusCode != consts.StatusBadRequest {
		t.Fatalf("normalizeSaveUser() error = %v, want bad request", err)
	}
}

func TestNormalizeSaveUserRequiresBanReason(t *testing.T) {
	service := &Service{now: time.Now}
	req := validSaveUserRequest()
	status := 0
	permanent := 1
	req.Status = &status
	req.BanPermanent = &permanent
	req.BanReason = "  "

	_, _, _, _, err := service.normalizeSaveUser(req, false)
	business, ok := apperrors.AsBusinessError(err)
	if !ok || business.StatusCode != consts.StatusBadRequest {
		t.Fatalf("normalizeSaveUser() error = %v, want bad request", err)
	}
}

func TestNormalizeSaveUserClearsBanFieldsWhenActivated(t *testing.T) {
	service := &Service{}
	req := validSaveUserRequest()
	permanent := 1
	req.BanPermanent = &permanent
	req.BanUntil = "2099-01-01 00:00:00"
	req.BanReason = "stale reason"

	normalized, _, status, _, err := service.normalizeSaveUser(req, false)
	if err != nil {
		t.Fatalf("normalizeSaveUser() error = %v", err)
	}
	if status != 1 || normalized.BanPermanent == nil || *normalized.BanPermanent != 0 || normalized.BanUntil != "" || normalized.BanReason != "" {
		t.Fatalf("active account retained ban values: %+v", normalized)
	}
}

func validSaveUserRequest() SaveUserRequest {
	points := int64(10)
	status := 1
	return SaveUserRequest{
		Balance:  "0",
		HomePath: "/console/workspace",
		Points:   &points,
		RealName: "Admin",
		RoleIDs:  []int64{1},
		Status:   &status,
		Username: "admin",
	}
}

func TestNormalizeSaveUserPreservesOmittedAssetsOnUpdate(t *testing.T) {
	service := &Service{}
	request := validSaveUserRequest()
	request.Balance = nil
	request.Points = nil
	normalized, _, _, _, err := service.normalizeSaveUser(request, false)
	if err != nil {
		t.Fatal(err)
	}
	if normalized.Balance != nil || normalized.Points != nil {
		t.Fatal("omitted assets must remain omitted on update")
	}
}

func TestNormalizeSaveUserStillDefaultsAssetsOnCreate(t *testing.T) {
	service := &Service{}
	request := validSaveUserRequest()
	request.Balance = nil
	request.Points = nil
	request.Password = "Sandbox-Only-User-42!"
	normalized, _, _, points, err := service.normalizeSaveUser(request, true)
	if err != nil {
		t.Fatal(err)
	}
	if normalized.Balance != "0.0000" || points != 0 {
		t.Fatal("new users must still start with zero assets when omitted")
	}
}

func TestNormalizeSaveUserPreservesExplicitZeroAssets(t *testing.T) {
	service := &Service{}
	request := validSaveUserRequest()
	zero := int64(0)
	request.Balance = "0"
	request.Points = &zero
	normalized, _, _, points, err := service.normalizeSaveUser(request, false)
	if err != nil {
		t.Fatal(err)
	}
	if normalized.Balance != "0.0000" || normalized.Points == nil || *normalized.Points != 0 || points != 0 {
		t.Fatal("explicit zero must not be mistaken for an omitted field")
	}
}
