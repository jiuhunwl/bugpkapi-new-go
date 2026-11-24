package gateway

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestCombinePackageQPSTakesHighestAndHonorsUnlimited(t *testing.T) {
	tests := []struct {
		name       string
		global     int
		globalOK   bool
		interfaceQ int
		ifaceOK    bool
		want       int
		wantFound  bool
	}{
		{name: "none", wantFound: false},
		{name: "global only", global: 10, globalOK: true, want: 10, wantFound: true},
		{name: "interface higher", global: 10, globalOK: true, interfaceQ: 50, ifaceOK: true, want: 50, wantFound: true},
		{name: "global higher", global: 50, globalOK: true, interfaceQ: 10, ifaceOK: true, want: 50, wantFound: true},
		{name: "global unlimited", global: 0, globalOK: true, interfaceQ: 99, ifaceOK: true, want: 0, wantFound: true},
		{name: "interface unlimited", global: 99, globalOK: true, interfaceQ: 0, ifaceOK: true, want: 0, wantFound: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, found := combinePackageQPS(test.global, test.globalOK, test.interfaceQ, test.ifaceOK)
			if got != test.want || found != test.wantFound {
				t.Fatalf("combinePackageQPS() = %d,%v; want %d,%v", got, found, test.want, test.wantFound)
			}
		})
	}
}

func newQPSPriorityService(t *testing.T) *Service {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	now := time.Unix(1_820_000_000, 0)
	return &Service{rdb: client, now: func() time.Time { return now }}
}

func TestReserveRequestQPSAppliesAccountAndInterfaceCaps(t *testing.T) {
	service := newQPSPriorityService(t)
	api := InterfaceConfig{ID: 801, UserQPSLimit: 3}
	ctx := context.Background()
	// Account cap 2 is lower than the interface cap 3: the third call must be
	// rejected by the account bucket even though the interface bucket has room.
	for i := 0; i < 2; i++ {
		if _, err := service.reserveRequestQPS(ctx, 900, api, BillingReservation{QPSLimit: 2}); err != nil {
			t.Fatalf("call %d should pass: %v", i+1, err)
		}
	}
	if _, err := service.reserveRequestQPS(ctx, 900, api, BillingReservation{QPSLimit: 2}); err == nil {
		t.Fatal("account QPS cap must reject the third call")
	}
}

func TestReserveRequestQPSInterfaceCapStillWinsOverHigherAccountQPS(t *testing.T) {
	service := newQPSPriorityService(t)
	api := InterfaceConfig{ID: 802, UserQPSLimit: 2}
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if _, err := service.reserveRequestQPS(ctx, 901, api, BillingReservation{QPSLimit: 50}); err != nil {
			t.Fatalf("call %d should pass: %v", i+1, err)
		}
	}
	if _, err := service.reserveRequestQPS(ctx, 901, api, BillingReservation{QPSLimit: 50}); err == nil {
		t.Fatal("interface QPS cap must reject the third call")
	}
}
