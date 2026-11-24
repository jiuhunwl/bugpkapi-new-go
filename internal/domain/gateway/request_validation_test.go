package gateway

import (
	"errors"
	"net/url"
	"strings"
	"testing"

	apperrors "bugpk-api/internal/pkg/errors"

	hertzapp "github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

func TestValidateOpenQueryAcceptsBoundedParameters(t *testing.T) {
	query := url.Values{"city": {"shanghai"}, "tag": {"a", "b"}}
	if err := validateOpenQuery(query); err != nil {
		t.Fatalf("validateOpenQuery() error = %v", err)
	}
}

func TestValidateOpenQueryRejectsExcessiveValueCount(t *testing.T) {
	values := make([]string, maxOpenQueryValueCount+1)
	if err := validateOpenQuery(url.Values{"value": values}); err == nil {
		t.Fatal("validateOpenQuery accepted too many values")
	}
}

func TestValidateOpenQueryRejectsOversizedNameAndValue(t *testing.T) {
	if err := validateOpenQuery(url.Values{strings.Repeat("n", maxOpenQueryNameBytes+1): {"ok"}}); err == nil {
		t.Fatal("validateOpenQuery accepted an oversized name")
	}
	if err := validateOpenQuery(url.Values{"value": {strings.Repeat("v", maxOpenQueryValueBytes+1)}}); err == nil {
		t.Fatal("validateOpenQuery accepted an oversized value")
	}
}

func TestOpenRequestRejectsMalformedAndOversizedQuery(t *testing.T) {
	for name, rawQuery := range map[string]string{
		"malformed": "%zz=bad",
		"oversized": "value=" + strings.Repeat("x", maxOpenQueryBytes),
	} {
		t.Run(name, func(t *testing.T) {
			ctx := newOpenRequestContext("/?" + rawQuery)
			if _, err := openRequest(ctx); err == nil {
				t.Fatal("openRequest accepted an invalid query")
			}
		})
	}
}

func TestOpenRequestRejectsOversizedHeaders(t *testing.T) {
	ctx := newOpenRequestContext("/?value=ok")
	ctx.Request.Header.Set("X-Large", strings.Repeat("x", maxOpenHeaderBytes+1))
	_, err := openRequest(ctx)
	var business *apperrors.BusinessError
	if !errors.As(err, &business) || business.StatusCode != consts.StatusRequestHeaderFieldsTooLarge {
		t.Fatalf("oversized header error = %v", err)
	}
}

func TestOpenRequestRejectsDuplicateAPIKeyHeaders(t *testing.T) {
	ctx := newOpenRequestContext("/?value=ok")
	ctx.Request.Header.Add("X-API-Key", "bp_live_first")
	ctx.Request.Header.Add("X-API-Key", "bp_live_second")
	_, err := openRequest(ctx)
	var business *apperrors.BusinessError
	if !errors.As(err, &business) || business.StatusCode != consts.StatusBadRequest {
		t.Fatalf("duplicate API key header error = %v", err)
	}
}

func TestOpenRequestRejectsOversizedBodyBeforeRead(t *testing.T) {
	ctx := newOpenRequestContext("/?value=ok")
	ctx.Request.SetMethod("POST")
	ctx.Request.Header.SetContentLength(maxOpenRequestBodyBytes + 1)

	_, err := openRequest(ctx)
	var business *apperrors.BusinessError
	if !errors.As(err, &business) || business.StatusCode != consts.StatusRequestEntityTooLarge {
		t.Fatalf("oversized Content-Length error = %v", err)
	}
}

func TestOpenRequestRejectsOversizedBodyWithoutTrustedLength(t *testing.T) {
	ctx := newOpenRequestContext("/?value=ok")
	ctx.Request.SetMethod("POST")
	ctx.Request.SetBodyString(strings.Repeat("x", maxOpenRequestBodyBytes+1))
	ctx.Request.Header.SetContentLength(-1)

	_, err := openRequest(ctx)
	var business *apperrors.BusinessError
	if !errors.As(err, &business) || business.StatusCode != consts.StatusRequestEntityTooLarge {
		t.Fatalf("oversized streamed body error = %v", err)
	}
}

func TestOpenRequestRejectsAmbiguousJSONBodies(t *testing.T) {
	tests := map[string]struct {
		body        string
		messagePart string
	}{
		"duplicate nested field": {
			body:        `{"outer":{"id":1,"id":2}}`,
			messagePart: "JSON字段重复: id",
		},
		"malformed syntax": {
			body:        `{"id":`,
			messagePart: "JSON语法错误",
		},
		"multiple top-level values": {
			body:        `{"id":1} {"id":2}`,
			messagePart: "JSON包含多个顶层值",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			ctx := newOpenRequestContext("/")
			ctx.Request.SetMethod("POST")
			ctx.Request.Header.Set("Content-Type", "application/json; charset=utf-8")
			ctx.Request.SetBodyString(test.body)
			_, err := openRequest(ctx)
			var business *apperrors.BusinessError
			if !errors.As(err, &business) || business.StatusCode != consts.StatusBadRequest {
				t.Fatalf("ambiguous JSON error = %v", err)
			}
			if !strings.Contains(err.Error(), test.messagePart) {
				t.Fatalf("ambiguous JSON error %q does not contain %q", err, test.messagePart)
			}
		})
	}
}

func TestOpenRequestAcceptsUnambiguousJSONAndJSONSuffixMediaType(t *testing.T) {
	ctx := newOpenRequestContext("/")
	ctx.Request.SetMethod("POST")
	ctx.Request.Header.Set("Content-Type", "application/problem+json")
	ctx.Request.SetBodyString(`{"outer":{"id":9007199254740993},"items":[1,2,3]}`)
	request, err := openRequest(ctx)
	if err != nil {
		t.Fatalf("valid JSON request was rejected: %v", err)
	}
	if len(request.Body) == 0 {
		t.Fatal("valid JSON request body was lost")
	}
}

func TestOpenRequestDoesNotApplyJSONParserToOtherMediaTypes(t *testing.T) {
	ctx := newOpenRequestContext("/")
	ctx.Request.SetMethod("POST")
	ctx.Request.Header.Set("Content-Type", "text/plain")
	ctx.Request.SetBodyString(`{"id":1,"id":2}`)
	if _, err := openRequest(ctx); err != nil {
		t.Fatalf("non-JSON interface body was rejected: %v", err)
	}
}

func TestOpenRequestRejectsExcessiveJSONNesting(t *testing.T) {
	ctx := newOpenRequestContext("/")
	ctx.Request.SetMethod("POST")
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Request.SetBodyString(strings.Repeat("[", maxOpenJSONNestingDepth+1) + "0" + strings.Repeat("]", maxOpenJSONNestingDepth+1))
	_, err := openRequest(ctx)
	var business *apperrors.BusinessError
	if !errors.As(err, &business) || business.StatusCode != consts.StatusBadRequest {
		t.Fatalf("deep JSON error = %v", err)
	}
	if !strings.Contains(err.Error(), "JSON嵌套层级") {
		t.Fatalf("deep JSON error is not descriptive: %v", err)
	}
}

func TestOpenRequestUsesConfiguredClientIPResolver(t *testing.T) {
	ctx := newOpenRequestContext("/?value=ok")
	request, err := openRequest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if request.ClientIP != "1.2.3.4" {
		t.Fatalf("ClientIP = %q", request.ClientIP)
	}
}

func newOpenRequestContext(uri string) *hertzapp.RequestContext {
	ctx := hertzapp.NewContext(0)
	ctx.SetClientIPFunc(func(*hertzapp.RequestContext) string { return "1.2.3.4" })
	ctx.Request.SetRequestURI(uri)
	ctx.Request.SetMethod("GET")
	return ctx
}
