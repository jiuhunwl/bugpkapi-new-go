package interfaceapi

import (
	"strings"
	"testing"

	"bugpk-api/internal/pkg/jsoncleaning"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

func TestPrepareInvokeResponseCleansEnabledJSON(t *testing.T) {
	raw := `{"result":{"videoUrl":"https://cdn.example/video.mp4"}}`
	result := InvokeResult{
		Body:         raw,
		ResponseSize: int64(len(raw)),
		StatusCode:   consts.StatusOK,
	}
	passed, err := prepareInvokeResponse(1, UpstreamConfig{}, &result)
	if err != nil || !passed || !strings.Contains(result.Body, `"type":"video"`) {
		t.Fatalf("response was not normalized: passed=%v err=%v body=%s", passed, err, result.Body)
	}
	if result.ResponseSize != int64(len(raw)) || !result.ResponseNormalized {
		t.Fatalf("original response metadata was lost: size=%d normalized=%v", result.ResponseSize, result.ResponseNormalized)
	}
}

func TestPrepareInvokeResponseAppliesConfiguredFieldMappingsBeforeNormalization(t *testing.T) {
	raw := `{"payload":{"resource_link":"https://cdn.example/video.mp4"}}`
	result := InvokeResult{Body: raw, ResponseSize: int64(len(raw)), StatusCode: consts.StatusOK}
	passed, err := prepareInvokeResponse(1, UpstreamConfig{ResponseFieldMappings: []jsoncleaning.FieldMapping{
		{Source: "payload", Target: "data"},
		{Source: "resource_link", Target: "url"},
	}}, &result)
	if err != nil || !passed || !strings.Contains(result.Body, `"url":"https://cdn.example/video.mp4"`) {
		t.Fatalf("mapped response was not normalized: passed=%v err=%v body=%s", passed, err, result.Body)
	}
}

func TestNormalizedInvokeResponseIsNotMarkedTruncatedOnlyBecauseItIsShorter(t *testing.T) {
	raw := `{"result":{"videoUrl":"https://cdn.example/video.mp4"}}`
	result := InvokeResult{Body: raw, ResponseSize: int64(len(raw)), StatusCode: consts.StatusOK}
	passed, err := prepareInvokeResponse(1, UpstreamConfig{}, &result)
	if err != nil || !passed {
		t.Fatalf("prepareInvokeResponse passed=%v err=%v", passed, err)
	}
	if responseWasTruncatedForResult(result.Body, result.ResponseSize, result.ResponseNormalized) {
		t.Fatal("normalization length change was reported as persistence truncation")
	}
}

func TestPrepareInvokeResponsePassesThroughWhenDisabled(t *testing.T) {
	raw := `{"result":{"videoUrl":"https://cdn.example/video.mp4"}}`
	result := InvokeResult{Body: raw, StatusCode: consts.StatusOK}
	passed, err := prepareInvokeResponse(0, UpstreamConfig{}, &result)
	if err != nil || !passed || result.Body != raw {
		t.Fatalf("disabled cleaning changed response: passed=%v err=%v body=%q", passed, err, result.Body)
	}
}

func TestPrepareInvokeResponseKeepsRawLegacySuccessForCustomPollingExpected(t *testing.T) {
	// The cleaner canonicalizes code=0 to code=200.  An explicit polling rule
	// expecting 0 must still honor the raw legacy success value.
	result := InvokeResult{
		Body:       `{"code":0,"data":{"videoUrl":"https://cdn.example/video.mp4"}}`,
		StatusCode: consts.StatusOK,
	}
	cfg := UpstreamConfig{PollingCheckEnabled: 1, PollingCheckField: "code", PollingCheckExpected: "0"}
	passed, err := prepareInvokeResponse(1, cfg, &result)
	if err != nil || !passed {
		t.Fatalf("raw legacy success was rejected after normalization: passed=%v err=%v body=%s", passed, err, result.Body)
	}
}

func TestPrepareInvokeResponseRejectsNormalizedNestedFailureOverRawSuccess(t *testing.T) {
	result := InvokeResult{
		Body:       `{"code":200,"payload":{"status":500,"message":"temporary failure"}}`,
		StatusCode: consts.StatusOK,
	}
	cfg := UpstreamConfig{PollingCheckEnabled: 1, PollingCheckField: "code", PollingCheckExpected: "200"}
	passed, err := prepareInvokeResponse(1, cfg, &result)
	if err != nil {
		t.Fatalf("unexpected normalization error: %v", err)
	}
	if passed {
		t.Fatalf("normalized nested failure was accepted: body=%s", result.Body)
	}
}

func TestPrepareInvokeResponseRejectsMalformedEnabledJSON(t *testing.T) {
	result := InvokeResult{Body: `{"data":`, StatusCode: consts.StatusOK}
	passed, err := prepareInvokeResponse(1, UpstreamConfig{}, &result)
	if err == nil || passed || !strings.Contains(err.Error(), "上游响应清洗失败") {
		t.Fatalf("malformed response result: passed=%v err=%v", passed, err)
	}
}

func TestPrepareInvokeResponseRejectsAutoCheckWhenNormalizedEnvelopeExposesFailureCode(t *testing.T) {
	// The legacy automatic check intentionally accepts a valid JSON envelope
	// that does not expose the configured field. Cleaning, however, derives a
	// canonical envelope and may discover the nested failure code. That
	// normalized result must be authoritative; a raw pass must not mask it.
	result := InvokeResult{
		Body:       `{"payload":{"status":500,"message":"temporary failure"}}`,
		StatusCode: consts.StatusOK,
	}
	cfg := UpstreamConfig{
		PollingCheckEnabled:  1,
		PollingCheckField:    "code",
		PollingCheckExpected: "200",
		pollingCheckAuto:     true,
	}
	passed, err := prepareInvokeResponse(1, cfg, &result)
	if err != nil {
		t.Fatalf("unexpected normalization error: %v", err)
	}
	if passed {
		t.Fatalf("automatic polling check accepted normalized failure envelope: body=%s", result.Body)
	}
}

func TestPrepareInvokeResponseRejectsRawSuccessWhenNormalizedEnvelopeExposesFailureCode(t *testing.T) {
	// A successful outer wrapper can still contain a failed nested response.
	// The normalized envelope is authoritative for polling and must trigger the
	// next candidate instead of allowing rawPassed to mask the failure.
	result := InvokeResult{
		Body:       `{"code":200,"payload":{"status":500,"message":"temporary failure"}}`,
		StatusCode: consts.StatusOK,
	}
	cfg := UpstreamConfig{
		PollingCheckEnabled:  1,
		PollingCheckField:    "code",
		PollingCheckExpected: "200",
		pollingCheckAuto:     true,
	}
	passed, err := prepareInvokeResponse(1, cfg, &result)
	if err != nil {
		t.Fatalf("unexpected normalization error: %v", err)
	}
	if passed {
		t.Fatalf("raw success masked normalized failure envelope: body=%s", result.Body)
	}
}
