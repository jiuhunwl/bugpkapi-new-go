package observability

import (
	"errors"
	"strings"
	"testing"
	"time"

	apperrors "bugpk-api/internal/pkg/errors"

	hertzapp "github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

func callLogParamsForQuery(query string) (CallLogListParams, error) {
	ctx := hertzapp.NewContext(0)
	ctx.Request.SetRequestURI("/call-log/list?" + query)
	return callLogParams(ctx)
}

func TestCallLogParamsParsesExactFilters(t *testing.T) {
	params, err := callLogParamsForQuery("traceId=abc123&clientIp=1.2.3.4&apiCode=weather.v1")
	if err != nil {
		t.Fatalf("callLogParams returned error: %v", err)
	}
	if params.TraceID != "abc123" || params.ClientIP != "1.2.3.4" || params.APICode != "weather.v1" {
		t.Fatalf("exact filters not parsed: %+v", params)
	}
}

func TestCallLogParamsRejectsOverlongExactFilters(t *testing.T) {
	long := strings.Repeat("a", 65)
	if _, err := callLogParamsForQuery("traceId=" + long); err == nil {
		t.Fatalf("expected 400 for overlong traceId")
	}
}

func TestCallLogParamsParsesBillingOnly(t *testing.T) {
	params, err := callLogParamsForQuery("billingOnly=1")
	if err != nil {
		t.Fatalf("callLogParams returned error: %v", err)
	}
	if !params.BillingOnly {
		t.Fatalf("billingOnly was not parsed: %+v", params)
	}
	params, err = callLogParamsForQuery("billingOnly=0")
	if err != nil {
		t.Fatalf("callLogParams returned error: %v", err)
	}
	if params.BillingOnly {
		t.Fatalf("billingOnly=0 should disable the filter: %+v", params)
	}
}

func TestCallLogParamsRejectsInvalidFilters(t *testing.T) {
	tests := []string{
		"/?success=abc",
		"/?billable=2",
		"/?billingOnly=2",
		"/?apiKeyId=-1",
		"/?interfaceId=abc",
		"/?page=0",
		"/?pageSize=201",
		"/?minElapsedMs=20&maxElapsedMs=10",
		"/?chargeType=UNKNOWN",
		"/?billingStatus=UNKNOWN",
		"/?unmasked=maybe",
		"/?startTime=not-a-time",
		"/?startTime=2026-01-02%2000:00:00&endTime=2026-01-01%2000:00:00",
	}
	for _, requestURI := range tests {
		ctx := hertzapp.NewContext(0)
		ctx.Request.SetRequestURI(requestURI)
		_, err := callLogParams(ctx)
		if err == nil {
			t.Fatalf("callLogParams(%q) accepted an invalid filter", requestURI)
		}
		var business *apperrors.BusinessError
		if !errors.As(err, &business) || business.StatusCode != consts.StatusBadRequest {
			t.Fatalf("callLogParams(%q) error = %#v, want HTTP 400", requestURI, err)
		}
	}
}

func TestCallLogParamsNormalizesValidFilters(t *testing.T) {
	ctx := hertzapp.NewContext(0)
	ctx.Request.SetRequestURI("/?success=1&billable=0&apiKeyId=12&interfaceId=34&userId=56&page=2&pageSize=50&chargeType=points&billingStatus=confirmed&startTime=2026-01-01T00:00:00%2B08:00&endTime=2026-01-02%2000:00:00")

	params, err := callLogParams(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if params.Success == nil || *params.Success != 1 || params.Billable == nil || *params.Billable != 0 {
		t.Fatalf("binary filters = success:%v billable:%v", params.Success, params.Billable)
	}
	if params.APIKeyID != 12 || params.InterfaceID != 34 || params.UserID != 56 {
		t.Fatalf("IDs were not parsed: %+v", params)
	}
	if params.Page != 2 || params.PageSize != 50 {
		t.Fatalf("pagination = %d/%d", params.Page, params.PageSize)
	}
	if params.ChargeType != "POINTS" || params.BillingStatus != "CONFIRMED" {
		t.Fatalf("billing filters were not normalized: %+v", params)
	}
	parsedStart, _ := time.Parse(time.RFC3339, "2026-01-01T00:00:00+08:00")
	expectedStart := parsedStart.In(time.FixedZone("expected business +08", 8*60*60)).Format("2006-01-02 15:04:05")
	if params.StartTime != expectedStart || params.EndTime != "2026-01-02 00:00:00" {
		t.Fatalf("times were not normalized: %q - %q", params.StartTime, params.EndTime)
	}
}

func TestNormalizeCallLogFilterWindowBoundsFuzzySearch(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.FixedZone("expected business +08", 8*60*60))
	params := CallLogListParams{Keyword: "trace"}
	if err := normalizeCallLogFilterWindow(&params, now); err != nil {
		t.Fatal(err)
	}
	if params.StartTime != "2026-06-21 12:00:00" || params.EndTime != "2026-07-21 12:00:00" {
		t.Fatalf("default fuzzy window = %q - %q", params.StartTime, params.EndTime)
	}

	params = CallLogListParams{
		Region:    "Beijing",
		StartTime: "2026-01-01 00:00:00",
		EndTime:   "2026-03-01 00:00:00",
	}
	if err := normalizeCallLogFilterWindow(&params, now); err == nil {
		t.Fatal("fuzzy filter accepted a time range longer than 31 days")
	}
}

func TestNormalizeCallLogFilterWindowBoundsElapsedAndStatusFilters(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.FixedZone("expected business +08", 8*60*60))

	tests := []CallLogListParams{
		{MinElapsedMs: 500},
		{MaxElapsedMs: 1200},
		{Success: func() *int { value := 0; return &value }()},
		{Billable: func() *int { value := 1; return &value }()},
		{ChargeType: "POINTS"},
		{BillingStatus: "CONFIRMED"},
	}
	for index, params := range tests {
		if err := normalizeCallLogFilterWindow(&params, now); err != nil {
			t.Fatalf("case %d: normalizeCallLogFilterWindow() error = %v", index, err)
		}
		if params.StartTime != "2026-06-21 12:00:00" || params.EndTime != "2026-07-21 12:00:00" {
			t.Fatalf("case %d: default filter window = %q - %q", index, params.StartTime, params.EndTime)
		}
	}
}

func TestNormalizeCallLogFilterWindowDoesNotRestrictIndexedOnlyFilters(t *testing.T) {
	params := CallLogListParams{APIKeyID: 1, InterfaceID: 2, UserID: 3}
	if err := normalizeCallLogFilterWindow(&params, time.Now()); err != nil {
		t.Fatal(err)
	}
	if params.StartTime != "" || params.EndTime != "" {
		t.Fatalf("indexed-only filter unexpectedly received a time window: %q - %q", params.StartTime, params.EndTime)
	}
}

func TestCallLogParamsRejectsTooShortFuzzyKeyword(t *testing.T) {
	if _, err := callLogParamsForQuery("keyword=a"); err == nil {
		t.Fatalf("expected 400 for single-ascii-char keyword")
	}
	if _, err := callLogParamsForQuery("keyword=ab"); err != nil {
		t.Fatalf("two-char keyword must be accepted: %v", err)
	}
	if _, err := callLogParamsForQuery("keyword=%E4%BA%AC"); err != nil {
		t.Fatalf("single CJK char keyword must be accepted: %v", err)
	}
}

func TestNormalizeCallLogFilterWindowDoesNotRestrictIndexedFilters(t *testing.T) {
	params := CallLogListParams{InterfaceID: 1, StartTime: "2020-01-01 00:00:00"}
	if err := normalizeCallLogFilterWindow(&params, time.Now()); err != nil {
		t.Fatal(err)
	}
	if params.EndTime != "" {
		t.Fatalf("indexed filter unexpectedly received an end time: %q", params.EndTime)
	}
}
