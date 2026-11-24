package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"bugpk-api/internal/pkg/masking"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

const (
	interfaceConfigInvalidationChannel        = "bugpk:gateway:interface-config:invalidate:v1"
	interfaceConfigInvalidationMessageVersion = 1
	interfaceConfigInvalidationPublishTimeout = 250 * time.Millisecond
	interfaceConfigInvalidationRetryDelay     = time.Second
	interfaceConfigInvalidationQueueSize      = 64
	interfaceConfigInvalidationMaxMessageSize = 4096
)

var errInterfaceConfigInvalidationTransportUnavailable = errors.New("interface config invalidation transport unavailable")

type interfaceConfigInvalidationMessage struct {
	Version  int      `json:"version"`
	APICodes []string `json:"apiCodes"`
}

type interfaceConfigInvalidationSubscription interface {
	Receive(context.Context) (string, error)
	Close() error
}

type interfaceConfigInvalidationTransport interface {
	Publish(context.Context, string, string) error
	Subscribe(context.Context, string) (interfaceConfigInvalidationSubscription, error)
}

type interfaceConfigInvalidationBus struct {
	ctx          context.Context
	cancel       context.CancelFunc
	transport    interfaceConfigInvalidationTransport
	invalidate   func(...string)
	log          *zap.Logger
	publishQueue chan string
	retryDelay   time.Duration
	done         chan struct{}
	workers      sync.WaitGroup
	closeOnce    sync.Once
	closed       atomic.Bool

	subscriptionMu sync.Mutex
	subscription   interfaceConfigInvalidationSubscription
}

// startInterfaceConfigInvalidationBus keeps the gateway's process-local cache
// synchronized across instances. Startup never waits for Redis connectivity;
// failures fall back to the cache's existing short TTL.
func startInterfaceConfigInvalidationBus(transport interfaceConfigInvalidationTransport, invalidate func(...string), log *zap.Logger) *interfaceConfigInvalidationBus {
	ctx, cancel := context.WithCancel(context.Background())
	bus := &interfaceConfigInvalidationBus{
		ctx:          ctx,
		cancel:       cancel,
		transport:    transport,
		invalidate:   invalidate,
		log:          log,
		publishQueue: make(chan string, interfaceConfigInvalidationQueueSize),
		retryDelay:   interfaceConfigInvalidationRetryDelay,
		done:         make(chan struct{}),
	}
	if transport == nil {
		close(bus.done)
		return bus
	}
	bus.workers.Add(2)
	go bus.publishLoop()
	go bus.subscribeLoop()
	go func() {
		bus.workers.Wait()
		close(bus.done)
	}()
	return bus
}

// Notify is the interface write callback. Local invalidation happens before
// the best-effort publish is queued, so Redis can never delay or reject a save.
func (b *interfaceConfigInvalidationBus) Notify(oldAPICode, newAPICode string) {
	if b == nil {
		return
	}
	apiCodes := normalizeInterfaceConfigInvalidationCodes(oldAPICode, newAPICode)
	if len(apiCodes) == 0 {
		return
	}
	if b.invalidate != nil {
		b.invalidate(apiCodes...)
	}
	if b.transport == nil || b.closed.Load() {
		return
	}
	payload, err := json.Marshal(interfaceConfigInvalidationMessage{
		Version:  interfaceConfigInvalidationMessageVersion,
		APICodes: apiCodes,
	})
	if err != nil {
		b.warn("encode", err)
		return
	}
	select {
	case b.publishQueue <- string(payload):
	default:
		b.warn("enqueue", errors.New("interface config invalidation publish queue is full"))
	}
}

func (b *interfaceConfigInvalidationBus) publishLoop() {
	defer b.workers.Done()
	for {
		select {
		case <-b.ctx.Done():
			return
		case payload := <-b.publishQueue:
			publishCtx, cancel := context.WithTimeout(b.ctx, interfaceConfigInvalidationPublishTimeout)
			err := b.transport.Publish(publishCtx, interfaceConfigInvalidationChannel, payload)
			cancel()
			if err != nil && b.ctx.Err() == nil {
				b.warn("publish", err)
			}
		}
	}
}

func (b *interfaceConfigInvalidationBus) subscribeLoop() {
	defer b.workers.Done()
	for b.ctx.Err() == nil {
		subscription, err := b.transport.Subscribe(b.ctx, interfaceConfigInvalidationChannel)
		if err != nil {
			if b.ctx.Err() != nil {
				return
			}
			b.warn("subscribe", err)
			if !b.waitBeforeResubscribe() {
				return
			}
			continue
		}
		if !b.installSubscription(subscription) {
			if subscription != nil {
				_ = subscription.Close()
			}
			return
		}
		// Pub/Sub does not replay messages published while this process was
		// disconnected. Clear every local entry after each successful subscribe
		// so a reconnect cannot keep serving configuration that changed during
		// that gap; normal per-interface messages keep subsequent invalidation
		// precise.
		if b.invalidate != nil {
			b.invalidate("*")
		}
		err = b.consumeSubscription(subscription)
		b.releaseSubscription(subscription)
		if b.ctx.Err() != nil {
			return
		}
		if err != nil {
			b.warn("receive", err)
		}
		if !b.waitBeforeResubscribe() {
			return
		}
	}
}

func (b *interfaceConfigInvalidationBus) consumeSubscription(subscription interfaceConfigInvalidationSubscription) error {
	for {
		payload, err := subscription.Receive(b.ctx)
		if err != nil {
			return err
		}
		apiCodes, ok := decodeInterfaceConfigInvalidation(payload)
		if !ok || b.invalidate == nil {
			continue
		}
		b.invalidate(apiCodes...)
	}
}

func (b *interfaceConfigInvalidationBus) waitBeforeResubscribe() bool {
	delay := b.retryDelay
	if delay <= 0 {
		delay = interfaceConfigInvalidationRetryDelay
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-b.ctx.Done():
		return false
	}
}

func (b *interfaceConfigInvalidationBus) installSubscription(subscription interfaceConfigInvalidationSubscription) bool {
	if subscription == nil {
		return false
	}
	b.subscriptionMu.Lock()
	defer b.subscriptionMu.Unlock()
	if b.ctx.Err() != nil {
		return false
	}
	b.subscription = subscription
	return true
}

func (b *interfaceConfigInvalidationBus) releaseSubscription(subscription interfaceConfigInvalidationSubscription) {
	b.subscriptionMu.Lock()
	owned := b.subscription == subscription
	if owned {
		b.subscription = nil
	}
	b.subscriptionMu.Unlock()
	if owned {
		if err := subscription.Close(); err != nil && b.ctx.Err() == nil {
			b.warn("close subscription", err)
		}
	}
}

func (b *interfaceConfigInvalidationBus) closeActiveSubscription() {
	b.subscriptionMu.Lock()
	subscription := b.subscription
	b.subscription = nil
	b.subscriptionMu.Unlock()
	if subscription != nil {
		_ = subscription.Close()
	}
}

func (b *interfaceConfigInvalidationBus) Close(ctx context.Context) error {
	if b == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	b.closeOnce.Do(func() {
		b.closed.Store(true)
		b.cancel()
		b.closeActiveSubscription()
	})
	select {
	case <-b.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *interfaceConfigInvalidationBus) warn(operation string, err error) {
	if b == nil || b.log == nil || err == nil {
		return
	}
	b.log.Warn("interface config invalidation transport failure",
		zap.String("operation", operation),
		zap.String("error", masking.Error(err)),
	)
}

func normalizeInterfaceConfigInvalidationCodes(values ...string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func decodeInterfaceConfigInvalidation(payload string) ([]string, bool) {
	if len(payload) == 0 || len(payload) > interfaceConfigInvalidationMaxMessageSize {
		return nil, false
	}
	var message interfaceConfigInvalidationMessage
	if err := json.Unmarshal([]byte(payload), &message); err != nil || message.Version != interfaceConfigInvalidationMessageVersion {
		return nil, false
	}
	apiCodes := normalizeInterfaceConfigInvalidationCodes(message.APICodes...)
	return apiCodes, len(apiCodes) > 0
}

type redisInterfaceConfigInvalidationTransport struct {
	client *redis.Client
}

func newRedisInterfaceConfigInvalidationTransport(client *redis.Client) interfaceConfigInvalidationTransport {
	if client == nil {
		return nil
	}
	return &redisInterfaceConfigInvalidationTransport{client: client}
}

func (t *redisInterfaceConfigInvalidationTransport) Publish(ctx context.Context, channel, payload string) error {
	if t == nil || t.client == nil {
		return errInterfaceConfigInvalidationTransportUnavailable
	}
	return t.client.WithTimeout(interfaceConfigInvalidationPublishTimeout).Publish(ctx, channel, payload).Err()
}

func (t *redisInterfaceConfigInvalidationTransport) Subscribe(ctx context.Context, channel string) (interfaceConfigInvalidationSubscription, error) {
	if t == nil || t.client == nil {
		return nil, errInterfaceConfigInvalidationTransportUnavailable
	}
	pubsub := t.client.Subscribe(ctx, channel)
	confirmation, err := pubsub.Receive(ctx)
	if err != nil {
		_ = pubsub.Close()
		return nil, err
	}
	subscription, ok := confirmation.(*redis.Subscription)
	if !ok || subscription.Kind != "subscribe" || subscription.Channel != channel || subscription.Count < 1 {
		_ = pubsub.Close()
		return nil, errors.New("interface config invalidation subscription was not confirmed")
	}
	return &redisInterfaceConfigInvalidationSubscription{pubsub: pubsub}, nil
}

type redisInterfaceConfigInvalidationSubscription struct {
	pubsub *redis.PubSub
}

func (s *redisInterfaceConfigInvalidationSubscription) Receive(ctx context.Context) (string, error) {
	if s == nil || s.pubsub == nil {
		return "", errInterfaceConfigInvalidationTransportUnavailable
	}
	message, err := s.pubsub.ReceiveMessage(ctx)
	if err != nil {
		return "", err
	}
	return message.Payload, nil
}

func (s *redisInterfaceConfigInvalidationSubscription) Close() error {
	if s == nil || s.pubsub == nil {
		return nil
	}
	return s.pubsub.Close()
}
