package openapi

import (
	"context"
	"fmt"
	"testing"

	"bugpk-api/internal/domain/gateway"
	"bugpk-api/internal/domain/rbac"
	userdomain "bugpk-api/internal/domain/user"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

type idempotencyAccountSource struct{}

func (idempotencyAccountSource) AccountSnapshot(context.Context, int64) (userdomain.AccountSnapshot, error) {
	return userdomain.AccountSnapshot{UserID: 42, Username: "review"}, nil
}

type idempotencyGatewaySource struct{ creates int }

func (g *idempotencyGatewaySource) CreateKey(context.Context, rbac.Principal, gateway.SaveKeyRequest) (gateway.CreateKeyResult, error) {
	g.creates++
	return gateway.CreateKeyResult{
		APIKeyItem: gateway.APIKeyItem{ID: fmt.Sprint(g.creates)},
	}, nil
}

func (*idempotencyGatewaySource) KeyDetail(context.Context, rbac.Principal, int64) (gateway.APIKeyItem, error) {
	return gateway.APIKeyItem{}, nil
}

func TestCreateAPIKeyHonorsClientIdempotencyKey(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	svc := NewService(nil, nil, rdb, nil, "test-only")
	gw := &idempotencyGatewaySource{}
	svc.SetSources(idempotencyAccountSource{}, nil, gw, nil, nil)
	ctx := context.Background()
	principal := OpenPrincipal{KeyID: 7, UserID: 42}
	req := CreateAPIKeyRequest{Name: "same-request"}
	cases := []struct {
		key, name, wantID string
		creates           int
	}{
		{"request-A", "initial request", "1", 1},
		{"request-A", "same-key retry", "1", 1},
		{"request-B", "independent request", "2", 2},
		{"", "without header", "3", 3},
		{"   ", "blank header", "4", 4},
		{"", "another request without header", "5", 5},
		{"request-A", "original retry after other creates", "1", 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := svc.CreateAPIKey(ctx, principal, req, tc.key)
			if err != nil {
				t.Fatal(err)
			}
			if got.ID != tc.wantID || gw.creates != tc.creates {
				t.Fatalf("id=%s creates=%d, want id=%s creates=%d", got.ID, gw.creates, tc.wantID, tc.creates)
			}
		})
	}
	// Expired keys start a new request after the documented 24-hour window.
	mr.FastForward(idempotencyTTL)
	got, err := svc.CreateAPIKey(ctx, principal, req, "request-A")
	if err != nil || got.ID != "6" {
		t.Fatalf("expired retry: %+v err=%v", got, err)
	}
}
