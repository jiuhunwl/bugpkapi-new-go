package auditprincipal

import (
	"context"
	"sync"
	"testing"
)

func TestCaptureRequiresMatchingRequestCredentialAndValidIdentity(t *testing.T) {
	Record(nil, "token", Identity{UserID: 1})
	for _, ctx := range []context.Context{context.Background(), WithRequest(nil, "")} {
		Record(ctx, "token", Identity{UserID: 1})
		if _, ok := FromContext(ctx); ok {
			t.Fatal("unbound capture accepted identity")
		}
	}
	ctx := WithRequest(context.Background(), "token")
	Record(ctx, "other", Identity{UserID: 1})
	Record(ctx, "token", Identity{})
	if _, ok := FromContext(ctx); ok {
		t.Fatal("wrong credential or zero identity was accepted")
	}
	Record(ctx, " token ", Identity{UserID: 42, Username: "user", Roles: []string{"user"}})
	got, ok := FromContext(ctx)
	if !ok || got.UserID != 42 {
		t.Fatal("verified identity was not retained")
	}
	if _, ok := FromContext(WithRequest(ctx, "token")); ok {
		t.Fatal("attribution escaped into another request")
	}
}
func TestCaptureKeepsFirstIdentityAndCopiesRoles(t *testing.T) {
	ctx := WithRequest(nil, "token")
	identity := Identity{UserID: 42, Roles: []string{"user"}}
	Record(ctx, "token", identity)
	identity.Roles[0] = "admin"
	Record(ctx, "token", Identity{UserID: 999, Roles: []string{"admin"}})
	first, _ := FromContext(ctx)
	if first.UserID != 42 || first.Roles[0] != "user" {
		t.Fatal("first identity changed")
	}
	first.Roles[0] = "admin"
	again, _ := FromContext(ctx)
	if again.Roles[0] != "user" {
		t.Fatal("returned roles alias stored attribution")
	}
}
func TestCaptureConcurrentPublishAndRead(t *testing.T) {
	ctx := WithRequest(nil, "token")
	var wait sync.WaitGroup
	for range 24 {
		wait.Go(func() {
			Record(ctx, "token", Identity{UserID: 42, Roles: []string{"user"}})
			got, ok := FromContext(ctx)
			if !ok || got.UserID != 42 || got.Roles[0] != "user" {
				t.Error("inconsistent audit capture")
				return
			}
			got.Roles[0] = "changed"
		})
	}
	wait.Wait()
}
