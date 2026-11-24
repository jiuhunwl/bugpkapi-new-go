package ticket

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"testing"
	"time"

	"bugpk-api/internal/config"

	"github.com/redis/go-redis/v9"
)

func TestTicketPublishBroadcastsToAdminAndOwnerRedisIntegration(t *testing.T) {
	if os.Getenv("TICKET_REDIS_INTEGRATION") != "1" {
		t.Skip("set TICKET_REDIS_INTEGRATION=1 to run")
	}
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(&redis.Options{
		Addr: cfg.Redis.Addr(), DB: cfg.Redis.Database, Password: cfg.Redis.Password,
	})
	t.Cleanup(func() { _ = client.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatal(err)
	}

	ownerID := time.Now().UnixNano()%1_000_000_000 + 10_000
	adminSubscription := client.Subscribe(ctx, adminTicketChannel())
	ownerSubscription := client.Subscribe(ctx, userTicketChannel(ownerID))
	t.Cleanup(func() {
		_ = adminSubscription.Close()
		_ = ownerSubscription.Close()
	})
	if _, err := adminSubscription.Receive(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := ownerSubscription.Receive(ctx); err != nil {
		t.Fatal(err)
	}

	service := &Service{rdb: client}
	service.publish(ctx, Event{
		ActorID:   "7",
		MessageID: "9",
		TicketID:  "11",
		Type:      "ticket.message",
		UserID:    formatTicketTestID(ownerID),
	})

	for name, subscription := range map[string]*redis.PubSub{
		"admin": adminSubscription,
		"owner": ownerSubscription,
	} {
		message, err := subscription.ReceiveMessage(ctx)
		if err != nil {
			t.Fatalf("%s subscription: %v", name, err)
		}
		var event Event
		if err := json.Unmarshal([]byte(message.Payload), &event); err != nil {
			t.Fatalf("%s payload: %v", name, err)
		}
		if event.Type != "ticket.message" || event.TicketID != "11" || event.UserID != formatTicketTestID(ownerID) || event.ID == "" || event.Timestamp == "" {
			t.Fatalf("%s event = %#v", name, event)
		}
	}
}

func formatTicketTestID(value int64) string {
	return strconv.FormatInt(value, 10)
}
