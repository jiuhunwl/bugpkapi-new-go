package gateway

import (
	"strings"
	"testing"

	"bugpk-api/internal/pkg/jsoncleaning"
	"bugpk-api/internal/pkg/proxypool"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

func TestPrepareOpenResponseCleansEnabledJSON(t *testing.T) {
	raw := `{"code":0,"data":{"video_url":"https://cdn.example/video.mp4"}}`
	result := OpenResult{
		Body:           raw,
		ResponseSize:   int64(len(raw)),
		ResponseStatus: consts.StatusOK,
	}
	passed, err := prepareOpenResponse(1, UpstreamConfig{}, &result)
	if err != nil || !passed {
		t.Fatalf("prepareOpenResponse passed=%v err=%v", passed, err)
	}
	if !strings.Contains(result.Body, `"code":200`) || !strings.Contains(result.Body, `"type":"video"`) {
		t.Fatalf("response was not normalized: %s", result.Body)
	}
	if result.ContentType != "application/json; charset=utf-8" {
		t.Fatalf("content type = %q", result.ContentType)
	}
	if result.ResponseSize != int64(len(raw)) || !result.ResponseNormalized {
		t.Fatalf("original response metadata was lost: size=%d normalized=%v", result.ResponseSize, result.ResponseNormalized)
	}
}

func TestPrepareOpenResponseAppliesConfiguredFieldMappingsBeforeNormalization(t *testing.T) {
	raw := `{"result":{"media_link":"https://cdn.example/video.mp4","headline":"demo"}}`
	result := OpenResult{Body: raw, ResponseSize: int64(len(raw)), ResponseStatus: consts.StatusOK}
	passed, err := prepareOpenResponse(1, UpstreamConfig{ResponseFieldMappings: []jsoncleaning.FieldMapping{
		{Source: "media_link", Target: "url"},
		{Source: "headline", Target: "title"},
	}}, &result)
	if err != nil || !passed {
		t.Fatalf("prepareOpenResponse passed=%v err=%v", passed, err)
	}
	if !strings.Contains(result.Body, `"url":"https://cdn.example/video.mp4"`) || !strings.Contains(result.Body, `"title":"demo"`) {
		t.Fatalf("mapped response was not normalized: %s", result.Body)
	}
}

func TestDecodeUpstreamConfigDocumentLoadsPrimaryAndPerNodeMappings(t *testing.T) {
	primary, configs := decodeUpstreamConfigDocument(`{
		"primaryResponseFieldMappings":[{"source":"payload","target":"data"}],
		"upstreams":[{"url":"https://node.example/api","responseFieldMappings":[{"source":"media","target":"url"}]}]
	}`)
	if len(primary) != 1 || primary[0].Target != "data" {
		t.Fatalf("primary mappings = %#v", primary)
	}
	if len(configs) != 1 || len(configs[0].ResponseFieldMappings) != 1 || configs[0].ResponseFieldMappings[0].Target != "url" {
		t.Fatalf("upstream configs = %#v", configs)
	}
}

func TestDecodeUpstreamConfigDocumentLoadsProxyPool(t *testing.T) {
	_, configs, proxyConfig := decodeUpstreamConfigDocumentFull(`{
		"proxyPoolConfig":{"enabled":1,"source":"FIXED","protocol":"SOCKS5","host":"proxy.example.com","port":1080},
		"upstreams":[{"url":"https://node.example/api"}]
	}`)
	if len(configs) != 1 || proxyConfig.Source != proxypool.SourceFixed || proxyConfig.Protocol != proxypool.ProtocolSOCKS5 {
		t.Fatalf("configs=%+v proxy=%+v", configs, proxyConfig)
	}
}

func TestNormalizedOpenResponseIsNotMarkedTruncatedOnlyBecauseItIsShorter(t *testing.T) {
	raw := `{"code":0,"data":{"video_url":"https://cdn.example/video.mp4"}}`
	result := OpenResult{Body: raw, ResponseSize: int64(len(raw)), ResponseStatus: consts.StatusOK}
	passed, err := prepareOpenResponse(1, UpstreamConfig{}, &result)
	if err != nil || !passed {
		t.Fatalf("prepareOpenResponse passed=%v err=%v", passed, err)
	}
	if responseWasTruncatedForResult(result.Body, result.ResponseSize, result.ResponseNormalized) {
		t.Fatal("normalization length change was reported as persistence truncation")
	}
}

func TestPrepareOpenResponseKeepsRawBodyWhenCleaningDisabled(t *testing.T) {
	raw := `{"code":0,"data":{"video_url":"https://cdn.example/video.mp4"}}`
	result := OpenResult{Body: raw, ResponseStatus: consts.StatusOK}
	passed, err := prepareOpenResponse(0, UpstreamConfig{}, &result)
	if err != nil || !passed || result.Body != raw {
		t.Fatalf("disabled cleaning changed response: passed=%v err=%v body=%q", passed, err, result.Body)
	}
}

func TestPrepareOpenResponseAllowsNormalizedSuccessCodeForPollingCheck(t *testing.T) {
	result := OpenResult{
		Body:           `{"code":0,"data":{"videoUrl":"https://cdn.example/video.mp4"}}`,
		ResponseStatus: consts.StatusOK,
	}
	passed, err := prepareOpenResponse(1, UpstreamConfig{PollingCheckEnabled: 1, PollingCheckField: "code", PollingCheckExpected: "200"}, &result)
	if err != nil || !passed || !strings.Contains(result.Body, `"code":200`) {
		t.Fatalf("normalized code did not pass: passed=%v err=%v body=%s", passed, err, result.Body)
	}
}

func TestPrepareOpenResponseKeepsRawLegacySuccessForCustomPollingExpected(t *testing.T) {
	// A caller may explicitly configure the legacy success code 0.  The
	// response cleaner rewrites that code to the canonical 200 envelope, but
	// that representation change must not turn an otherwise valid response
	// into a polling failure.
	result := OpenResult{
		Body:           `{"code":0,"data":{"videoUrl":"https://cdn.example/video.mp4"}}`,
		ResponseStatus: consts.StatusOK,
	}
	cfg := UpstreamConfig{PollingCheckEnabled: 1, PollingCheckField: "code", PollingCheckExpected: "0"}
	passed, err := prepareOpenResponse(1, cfg, &result)
	if err != nil || !passed {
		t.Fatalf("raw legacy success was rejected after normalization: passed=%v err=%v body=%s", passed, err, result.Body)
	}
}

func TestPrepareOpenResponseRejectsNormalizedNestedFailureOverRawSuccess(t *testing.T) {
	// Some providers wrap a failed operation in an outer code=200 envelope.
	// The cleaner discovers the nested status=500; that failure must trigger
	// polling instead of allowing the outer raw success to win.
	result := OpenResult{
		Body:           `{"code":200,"payload":{"status":500,"message":"temporary failure"}}`,
		ResponseStatus: consts.StatusOK,
	}
	cfg := UpstreamConfig{PollingCheckEnabled: 1, PollingCheckField: "code", PollingCheckExpected: "200"}
	passed, err := prepareOpenResponse(1, cfg, &result)
	if err != nil {
		t.Fatalf("unexpected normalization error: %v", err)
	}
	if passed {
		t.Fatalf("normalized nested failure was accepted: body=%s", result.Body)
	}
}

func TestPrepareOpenResponseRejectsAutoCheckWhenNormalizedEnvelopeExposesFailureCode(t *testing.T) {
	result := OpenResult{
		Body:           `{"payload":{"status":500,"message":"temporary failure"}}`,
		ResponseStatus: consts.StatusOK,
	}
	cfg := UpstreamConfig{
		PollingCheckEnabled:  1,
		PollingCheckField:    "code",
		PollingCheckExpected: "200",
		pollingCheckAuto:     true,
	}
	passed, err := prepareOpenResponse(1, cfg, &result)
	if err != nil {
		t.Fatalf("unexpected normalization error: %v", err)
	}
	if passed {
		t.Fatalf("automatic polling check accepted normalized failure envelope: body=%s", result.Body)
	}
}

func TestPrepareOpenResponseRejectsRawSuccessWhenNormalizedEnvelopeExposesFailureCode(t *testing.T) {
	// An outer success code must not hide a nested failure discovered by the
	// cleaner. This is the case that used to slip through when rawPassed was
	// accepted before inspecting the canonical envelope.
	result := OpenResult{
		Body:           `{"code":200,"payload":{"status":500,"message":"temporary failure"}}`,
		ResponseStatus: consts.StatusOK,
	}
	cfg := UpstreamConfig{
		PollingCheckEnabled:  1,
		PollingCheckField:    "code",
		PollingCheckExpected: "200",
		pollingCheckAuto:     true,
	}
	passed, err := prepareOpenResponse(1, cfg, &result)
	if err != nil {
		t.Fatalf("unexpected normalization error: %v", err)
	}
	if passed {
		t.Fatalf("raw success masked normalized failure envelope: body=%s", result.Body)
	}
}

func TestPrepareOpenResponseRejectsMalformedEnabledJSON(t *testing.T) {
	result := OpenResult{Body: `{"data":`, ResponseStatus: consts.StatusOK}
	passed, err := prepareOpenResponse(1, UpstreamConfig{}, &result)
	if err == nil || passed || !strings.Contains(err.Error(), "上游响应清洗失败") {
		t.Fatalf("malformed response result: passed=%v err=%v", passed, err)
	}
}
