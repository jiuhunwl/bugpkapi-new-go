package app

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

func TestInterfaceConfigInvalidationBroadcastReachesAnotherInstance(t *testing.T) {
	transport := newFakeInterfaceConfigInvalidationTransport()
	localEvents := make(chan []string, 4)
	remoteEvents := make(chan []string, 4)
	first := startInterfaceConfigInvalidationBus(transport, func(apiCodes ...string) {
		localEvents <- append([]string(nil), apiCodes...)
	}, zap.NewNop())
	second := startInterfaceConfigInvalidationBus(transport, func(apiCodes ...string) {
		remoteEvents <- append([]string(nil), apiCodes...)
	}, zap.NewNop())
	t.Cleanup(func() {
		closeInterfaceConfigInvalidationBus(t, first)
		closeInterfaceConfigInvalidationBus(t, second)
	})
	waitForInvalidationSubscribers(t, transport, 2)
	assertInvalidationEvent(t, localEvents, []string{"*"})
	assertInvalidationEvent(t, remoteEvents, []string{"*"})

	first.Notify(" Weather ", "SEARCH")
	assertInvalidationEvent(t, localEvents, []string{"weather", "search"})
	assertInvalidationEvent(t, remoteEvents, []string{"weather", "search"})
}

func TestInterfaceConfigInvalidationIgnoresMalformedMessages(t *testing.T) {
	transport := newFakeInterfaceConfigInvalidationTransport()
	events := make(chan []string, 8)
	bus := startInterfaceConfigInvalidationBus(transport, func(apiCodes ...string) {
		events <- append([]string(nil), apiCodes...)
	}, zap.NewNop())
	t.Cleanup(func() { closeInterfaceConfigInvalidationBus(t, bus) })
	waitForInvalidationSubscribers(t, transport, 1)
	assertInvalidationEvent(t, events, []string{"*"})

	for _, payload := range []string{
		`not-json`,
		`{"version":1}`,
		`{"version":2,"apiCodes":["weather"]}`,
		`{"version":1,"apiCodes":[" ",""]}`,
	} {
		transport.broadcast(payload)
	}
	transport.broadcast(`{"version":1,"apiCodes":[" Weather "]}`)

	assertInvalidationEvent(t, events, []string{"weather"})
	select {
	case unexpected := <-events:
		t.Fatalf("malformed invalidation message invoked callback: %v", unexpected)
	case <-time.After(25 * time.Millisecond):
	}
}

func TestInterfaceConfigInvalidationCloseReleasesSubscription(t *testing.T) {
	transport := newFakeInterfaceConfigInvalidationTransport()
	events := make(chan []string, 1)
	bus := startInterfaceConfigInvalidationBus(transport, func(apiCodes ...string) {
		events <- append([]string(nil), apiCodes...)
	}, zap.NewNop())
	waitForInvalidationSubscribers(t, transport, 1)
	assertInvalidationEvent(t, events, []string{"*"})

	closeInterfaceConfigInvalidationBus(t, bus)
	if got := transport.activeSubscriptions(); got != 0 {
		t.Fatalf("active subscriptions after close = %d, want 0", got)
	}
	if got := transport.closedSubscriptions(); got != 1 {
		t.Fatalf("closed subscriptions = %d, want 1", got)
	}
	transport.broadcast(`{"version":1,"apiCodes":["weather"]}`)
	select {
	case event := <-events:
		t.Fatalf("closed invalidation bus still received an event: %v", event)
	case <-time.After(25 * time.Millisecond):
	}
}

func TestInterfaceConfigInvalidationReconnectClearsMessagesMissedWhileDisconnected(t *testing.T) {
	transport := newFakeInterfaceConfigInvalidationTransport()
	events := make(chan []string, 4)
	bus := startInterfaceConfigInvalidationBus(transport, func(apiCodes ...string) {
		events <- append([]string(nil), apiCodes...)
	}, zap.NewNop())
	bus.retryDelay = time.Millisecond
	t.Cleanup(func() { closeInterfaceConfigInvalidationBus(t, bus) })

	waitForInvalidationSubscribers(t, transport, 1)
	assertInvalidationEvent(t, events, []string{"*"})

	transport.failSubscriptions(errors.New("redis connection lost"))
	waitForInvalidationSubscriptionCreations(t, transport, 2)
	assertInvalidationEvent(t, events, []string{"*"})
}

func TestRedisInterfaceConfigInvalidationSubscribeWaitsForConfirmation(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	subscribeReceived := make(chan struct{})
	releaseConfirmation := make(chan struct{})
	serverDone := make(chan error, 1)
	const channel = "test:interface-config-invalidation"
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			serverDone <- acceptErr
			return
		}
		defer connection.Close()
		reader := bufio.NewReader(connection)
		for {
			command, readErr := readRedisCommand(reader)
			if readErr != nil {
				serverDone <- readErr
				return
			}
			switch strings.ToLower(command) {
			case "hello":
				// go-redis probes RESP3 with HELLO even when Protocol is 2.
				// Answering "+OK" leaves the client waiting for a map reply, so it
				// never reaches SUBSCRIBE and dies on its own read timeout.
				// Reject the probe exactly like serveRedisHandshake does; the
				// client then falls back to RESP2 and issues SUBSCRIBE.
				if _, writeErr := connection.Write([]byte("-ERR unknown command 'hello'\r\n")); writeErr != nil {
					serverDone <- writeErr
					return
				}
			case "subscribe":
				close(subscribeReceived)
				<-releaseConfirmation
				_, writeErr := fmt.Fprintf(connection, "*3\r\n$9\r\nsubscribe\r\n$%d\r\n%s\r\n:1\r\n", len(channel), channel)
				serverDone <- writeErr
				return
			default:
				if _, writeErr := connection.Write([]byte("+OK\r\n")); writeErr != nil {
					serverDone <- writeErr
					return
				}
			}
		}
	}()

	client := redis.NewClient(&redis.Options{
		Addr:                  listener.Addr().String(),
		ContextTimeoutEnabled: true,
		DialTimeout:           time.Second,
		ReadTimeout:           time.Second,
		WriteTimeout:          time.Second,
		Protocol:              2,
		DisableIdentity:       true,
	})
	t.Cleanup(func() { _ = client.Close() })
	transport := newRedisInterfaceConfigInvalidationTransport(client)
	type subscribeResult struct {
		subscription interfaceConfigInvalidationSubscription
		err          error
	}
	result := make(chan subscribeResult, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() {
		subscription, subscribeErr := transport.Subscribe(ctx, channel)
		result <- subscribeResult{subscription: subscription, err: subscribeErr}
	}()

	select {
	case premature := <-result:
		if premature.subscription != nil {
			_ = premature.subscription.Close()
		}
		t.Fatalf("Subscribe returned before Redis confirmed the subscription: err=%v", premature.err)
	case <-subscribeReceived:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case premature := <-result:
		if premature.subscription != nil {
			_ = premature.subscription.Close()
		}
		t.Fatalf("Subscribe returned while Redis confirmation was still blocked: err=%v", premature.err)
	case <-time.After(25 * time.Millisecond):
	}
	close(releaseConfirmation)
	select {
	case completed := <-result:
		if completed.err != nil {
			t.Fatalf("Subscribe returned an error after confirmation: %v", completed.err)
		}
		if completed.subscription == nil {
			t.Fatal("Subscribe returned a nil subscription after confirmation")
		}
		_ = completed.subscription.Close()
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("fake Redis server error: %v", err)
	}
}

func TestInterfaceConfigInvalidationRedisFailuresDoNotBlockStartupOrLocalSave(t *testing.T) {
	publishStarted := make(chan struct{})
	localEvents := make(chan []string, 1)
	var publishOnce sync.Once
	transport := newFakeInterfaceConfigInvalidationTransport()
	transport.subscribeFn = func(ctx context.Context, _ string) (interfaceConfigInvalidationSubscription, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	transport.publishFn = func(ctx context.Context, _, _ string) error {
		publishOnce.Do(func() { close(publishStarted) })
		<-ctx.Done()
		return ctx.Err()
	}

	started := make(chan *interfaceConfigInvalidationBus, 1)
	go func() {
		started <- startInterfaceConfigInvalidationBus(transport, func(apiCodes ...string) {
			localEvents <- append([]string(nil), apiCodes...)
		}, zap.NewNop())
	}()
	var bus *interfaceConfigInvalidationBus
	select {
	case bus = <-started:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("a blocked Redis subscription delayed application startup")
	}

	notifyDone := make(chan struct{})
	go func() {
		bus.Notify(" Weather ", "")
		close(notifyDone)
	}()
	select {
	case <-notifyDone:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("a blocked Redis publish delayed the local interface save callback")
	}
	assertInvalidationEvent(t, localEvents, []string{"weather"})
	select {
	case <-publishStarted:
	case <-time.After(time.Second):
		t.Fatal("queued invalidation was not offered to Redis")
	}
	closeInterfaceConfigInvalidationBus(t, bus)
}

type fakeInterfaceConfigInvalidationTransport struct {
	mu            sync.Mutex
	subscriptions map[*fakeInterfaceConfigInvalidationSubscription]struct{}
	created       int
	closed        int
	publishFn     func(context.Context, string, string) error
	subscribeFn   func(context.Context, string) (interfaceConfigInvalidationSubscription, error)
}

func newFakeInterfaceConfigInvalidationTransport() *fakeInterfaceConfigInvalidationTransport {
	return &fakeInterfaceConfigInvalidationTransport{
		subscriptions: make(map[*fakeInterfaceConfigInvalidationSubscription]struct{}),
	}
}

func (f *fakeInterfaceConfigInvalidationTransport) Publish(ctx context.Context, channel, payload string) error {
	if f.publishFn != nil {
		return f.publishFn(ctx, channel, payload)
	}
	if channel != interfaceConfigInvalidationChannel {
		return errors.New("unexpected invalidation channel")
	}
	f.broadcast(payload)
	return nil
}

func (f *fakeInterfaceConfigInvalidationTransport) Subscribe(ctx context.Context, channel string) (interfaceConfigInvalidationSubscription, error) {
	if f.subscribeFn != nil {
		return f.subscribeFn(ctx, channel)
	}
	if channel != interfaceConfigInvalidationChannel {
		return nil, errors.New("unexpected invalidation channel")
	}
	subscription := &fakeInterfaceConfigInvalidationSubscription{
		messages: make(chan string, 32),
		errors:   make(chan error, 1),
		closed:   make(chan struct{}),
		owner:    f,
	}
	f.mu.Lock()
	f.subscriptions[subscription] = struct{}{}
	f.created++
	f.mu.Unlock()
	return subscription, nil
}

func (f *fakeInterfaceConfigInvalidationTransport) failSubscriptions(err error) {
	f.mu.Lock()
	subscriptions := make([]*fakeInterfaceConfigInvalidationSubscription, 0, len(f.subscriptions))
	for subscription := range f.subscriptions {
		subscriptions = append(subscriptions, subscription)
	}
	f.mu.Unlock()
	for _, subscription := range subscriptions {
		select {
		case subscription.errors <- err:
		case <-subscription.closed:
		}
	}
}

func (f *fakeInterfaceConfigInvalidationTransport) broadcast(payload string) {
	f.mu.Lock()
	subscriptions := make([]*fakeInterfaceConfigInvalidationSubscription, 0, len(f.subscriptions))
	for subscription := range f.subscriptions {
		subscriptions = append(subscriptions, subscription)
	}
	f.mu.Unlock()
	for _, subscription := range subscriptions {
		select {
		case subscription.messages <- payload:
		case <-subscription.closed:
		}
	}
}

func (f *fakeInterfaceConfigInvalidationTransport) remove(subscription *fakeInterfaceConfigInvalidationSubscription) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.subscriptions[subscription]; !ok {
		return
	}
	delete(f.subscriptions, subscription)
	f.closed++
}

func (f *fakeInterfaceConfigInvalidationTransport) activeSubscriptions() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.subscriptions)
}

func (f *fakeInterfaceConfigInvalidationTransport) closedSubscriptions() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

func (f *fakeInterfaceConfigInvalidationTransport) createdSubscriptions() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.created
}

type fakeInterfaceConfigInvalidationSubscription struct {
	messages  chan string
	errors    chan error
	closed    chan struct{}
	closeOnce sync.Once
	owner     *fakeInterfaceConfigInvalidationTransport
}

func (s *fakeInterfaceConfigInvalidationSubscription) Receive(ctx context.Context) (string, error) {
	select {
	case payload := <-s.messages:
		return payload, nil
	case err := <-s.errors:
		return "", err
	case <-s.closed:
		return "", errors.New("subscription closed")
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func waitForInvalidationSubscriptionCreations(t *testing.T, transport *fakeInterfaceConfigInvalidationTransport, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for transport.createdSubscriptions() < want {
		if time.Now().After(deadline) {
			t.Fatalf("subscription creations = %d, want at least %d", transport.createdSubscriptions(), want)
		}
		time.Sleep(time.Millisecond)
	}
}

func (s *fakeInterfaceConfigInvalidationSubscription) Close() error {
	s.closeOnce.Do(func() {
		close(s.closed)
		s.owner.remove(s)
	})
	return nil
}

func waitForInvalidationSubscribers(t *testing.T, transport *fakeInterfaceConfigInvalidationTransport, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for transport.activeSubscriptions() != want {
		if time.Now().After(deadline) {
			t.Fatalf("active subscriptions = %d, want %d", transport.activeSubscriptions(), want)
		}
		time.Sleep(time.Millisecond)
	}
}

func assertInvalidationEvent(t *testing.T, events <-chan []string, want []string) {
	t.Helper()
	select {
	case got := <-events:
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("invalidation event = %v, want %v", got, want)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for invalidation event %v", want)
	}
}

func closeInterfaceConfigInvalidationBus(t *testing.T, bus *interfaceConfigInvalidationBus) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := bus.Close(ctx); err != nil {
		t.Fatalf("close invalidation bus: %v", err)
	}
}
