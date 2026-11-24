package interfaceapi

import (
	"errors"
	"net/http"
	"testing"

	"bugpk-api/internal/pkg/domainrouting"
	apperrors "bugpk-api/internal/pkg/errors"
)

func TestResolveInterfaceDomainRouteHonorsRejectFallbackWhenNoRoutesExist(t *testing.T) {
	item := InterfaceItem{
		ID:         "995",
		RequestURL: "https://default.example/api",
		DomainRouting: DomainRoutingConfig{
			Enabled:  1,
			Fallback: domainrouting.FallbackReject,
		},
	}

	_, _, err := resolveInterfaceDomainRoute(item, InvokePayload{
		Method: http.MethodGet,
		QueryParams: map[string]any{
			"url": "https://v.example.com/resource",
		},
	})
	if err == nil {
		t.Fatal("enabled routing with REJECT fallback accepted a request without routes")
	}
	var business *apperrors.BusinessError
	if !errors.As(err, &business) || business.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("error = %T %v, want 422 BusinessError", err, err)
	}
}
