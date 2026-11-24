package gateway

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Key authentication is read on every public gateway request. The short TTL
// keeps revocations and account bans bounded while removing the database read
// from the steady-state path. Concurrent misses for one key are coalesced so
// an expiry boundary cannot create a connection burst.
const (
	keyAuthCacheTTL      = 2 * time.Second
	keyInterfaceCacheTTL = 2 * time.Second
)

type keyAuthCache struct {
	mu          sync.Mutex
	ttl         time.Duration
	clock       func() time.Time
	items       map[string]keyAuthCacheEntry
	loading     map[string]*keyAuthCacheLoad
	keyToHashes map[int64]map[string]struct{}
	epoch       uint64
}

type keyAuthCacheEntry struct {
	auth      KeyAuth
	expiresAt time.Time
}

type keyAuthCacheLoad struct {
	done  chan struct{}
	auth  KeyAuth
	err   error
	epoch uint64
}

var errKeyAuthCacheLoaderPanic = errors.New("key auth loader panicked")

func newKeyAuthCache(ttl time.Duration) *keyAuthCache {
	if ttl <= 0 {
		ttl = keyAuthCacheTTL
	}
	return &keyAuthCache{
		ttl:         ttl,
		clock:       time.Now,
		items:       make(map[string]keyAuthCacheEntry),
		loading:     make(map[string]*keyAuthCacheLoad),
		keyToHashes: make(map[int64]map[string]struct{}),
	}
}

func (c *keyAuthCache) get(now time.Time, hash string) (KeyAuth, bool) {
	if c == nil {
		return KeyAuth{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.items[hash]
	if !ok || now.After(entry.expiresAt) {
		if ok {
			c.deleteLocked(hash, entry.auth.ID)
		}
		return KeyAuth{}, false
	}
	return entry.auth, true
}

func (c *keyAuthCache) set(now time.Time, hash string, auth KeyAuth) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if previous, ok := c.items[hash]; ok && previous.auth.ID != auth.ID {
		c.deleteLocked(hash, previous.auth.ID)
	}
	c.items[hash] = keyAuthCacheEntry{auth: auth, expiresAt: now.Add(c.ttl)}
	hashes := c.keyToHashes[auth.ID]
	if hashes == nil {
		hashes = make(map[string]struct{})
		c.keyToHashes[auth.ID] = hashes
	}
	hashes[hash] = struct{}{}
}

func (c *keyAuthCache) deleteLocked(hash string, keyID int64) {
	delete(c.items, hash)
	if hashes := c.keyToHashes[keyID]; hashes != nil {
		delete(hashes, hash)
		if len(hashes) == 0 {
			delete(c.keyToHashes, keyID)
		}
	}
}

func (c *keyAuthCache) invalidateKey(keyID int64) {
	if c == nil || keyID <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.epoch++
	// Existing loads keep their private completion channels, but new requests
	// must not join work that started before the management change.
	c.loading = make(map[string]*keyAuthCacheLoad)
	for hash := range c.keyToHashes[keyID] {
		delete(c.items, hash)
	}
	delete(c.keyToHashes, keyID)
}

func (c *keyAuthCache) getOrLoad(
	ctx context.Context,
	now time.Time,
	hash string,
	loader func() (KeyAuth, error),
) (KeyAuth, error) {
	if c == nil {
		return loader()
	}
	for {
		c.mu.Lock()
		if entry, ok := c.items[hash]; ok && !now.After(entry.expiresAt) {
			c.mu.Unlock()
			return entry.auth, nil
		}
		if call, ok := c.loading[hash]; ok {
			c.mu.Unlock()
			select {
			case <-call.done:
				c.mu.Lock()
				stale := call.epoch != c.epoch
				c.mu.Unlock()
				if stale {
					continue
				}
				return call.auth, call.err
			case <-ctx.Done():
				return KeyAuth{}, ctx.Err()
			}
		}
		call := &keyAuthCacheLoad{done: make(chan struct{}), epoch: c.epoch}
		c.loading[hash] = call
		c.mu.Unlock()

		var panicValue any
		func() {
			defer func() { panicValue = recover() }()
			call.auth, call.err = loader()
		}()
		if panicValue != nil {
			call.auth = KeyAuth{}
			call.err = errKeyAuthCacheLoaderPanic
		}
		c.mu.Lock()
		stale := call.epoch != c.epoch
		if call.err == nil && !stale {
			if previous, ok := c.items[hash]; ok && previous.auth.ID != call.auth.ID {
				c.deleteLocked(hash, previous.auth.ID)
			}
			c.items[hash] = keyAuthCacheEntry{auth: call.auth, expiresAt: c.clock().Add(c.ttl)}
			hashes := c.keyToHashes[call.auth.ID]
			if hashes == nil {
				hashes = make(map[string]struct{})
				c.keyToHashes[call.auth.ID] = hashes
			}
			hashes[hash] = struct{}{}
		}
		if c.loading[hash] == call {
			delete(c.loading, hash)
		}
		close(call.done)
		c.mu.Unlock()
		if panicValue != nil {
			panic(panicValue)
		}
		if stale {
			if err := ctx.Err(); err != nil {
				return KeyAuth{}, err
			}
			continue
		}
		return call.auth, call.err
	}
}

type keyInterfaceCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	clock   func() time.Time
	items   map[int64]keyInterfaceCacheEntry
	loading map[int64]*keyInterfaceCacheLoad
	version map[int64]uint64
}

type keyInterfaceCacheEntry struct {
	interfaces map[int64]struct{}
	expiresAt  time.Time
}

type keyInterfaceCacheLoad struct {
	done       chan struct{}
	interfaces []int64
	err        error
	version    uint64
}

var errKeyInterfaceCacheLoaderPanic = errors.New("key interface loader panicked")

func newKeyInterfaceCache(ttl time.Duration) *keyInterfaceCache {
	if ttl <= 0 {
		ttl = keyInterfaceCacheTTL
	}
	return &keyInterfaceCache{
		ttl:     ttl,
		clock:   time.Now,
		items:   make(map[int64]keyInterfaceCacheEntry),
		loading: make(map[int64]*keyInterfaceCacheLoad),
		version: make(map[int64]uint64),
	}
}

func (c *keyInterfaceCache) get(now time.Time, keyID int64) (keyInterfaceCacheEntry, bool) {
	if c == nil {
		return keyInterfaceCacheEntry{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.items[keyID]
	if !ok || now.After(entry.expiresAt) {
		if ok {
			delete(c.items, keyID)
		}
		return keyInterfaceCacheEntry{}, false
	}
	return entry, true
}

func (c *keyInterfaceCache) set(now time.Time, keyID int64, interfaces []int64) {
	if c == nil {
		return
	}
	set := make(map[int64]struct{}, len(interfaces))
	for _, interfaceID := range interfaces {
		set[interfaceID] = struct{}{}
	}
	c.mu.Lock()
	c.items[keyID] = keyInterfaceCacheEntry{interfaces: set, expiresAt: now.Add(c.ttl)}
	c.mu.Unlock()
}

func (c *keyInterfaceCache) invalidateKey(keyID int64) {
	if c == nil || keyID <= 0 {
		return
	}
	c.mu.Lock()
	delete(c.items, keyID)
	c.version[keyID]++
	delete(c.loading, keyID)
	c.mu.Unlock()
}

func (c *keyInterfaceCache) allows(
	ctx context.Context,
	now time.Time,
	keyID, interfaceID int64,
	loader func() ([]int64, error),
) (bool, error) {
	if c == nil {
		interfaces, err := loader()
		if err != nil {
			return false, err
		}
		for _, candidate := range interfaces {
			if candidate == interfaceID {
				return true, nil
			}
		}
		return false, nil
	}
	for {
		if entry, ok := c.get(now, keyID); ok {
			_, allowed := entry.interfaces[interfaceID]
			return allowed, nil
		}

		c.mu.Lock()
		if entry, ok := c.items[keyID]; ok && !now.After(entry.expiresAt) {
			c.mu.Unlock()
			_, allowed := entry.interfaces[interfaceID]
			return allowed, nil
		}
		if call, ok := c.loading[keyID]; ok {
			c.mu.Unlock()
			select {
			case <-call.done:
				c.mu.Lock()
				stale := call.version != c.version[keyID]
				c.mu.Unlock()
				if stale {
					continue
				}
				if call.err != nil {
					return false, call.err
				}
				for _, candidate := range call.interfaces {
					if candidate == interfaceID {
						return true, nil
					}
				}
				return false, nil
			case <-ctx.Done():
				return false, ctx.Err()
			}
		}
		call := &keyInterfaceCacheLoad{
			done:    make(chan struct{}),
			version: c.version[keyID],
		}
		c.loading[keyID] = call
		c.mu.Unlock()

		var panicValue any
		func() {
			defer func() { panicValue = recover() }()
			call.interfaces, call.err = loader()
		}()
		if panicValue != nil {
			call.interfaces = nil
			call.err = errKeyInterfaceCacheLoaderPanic
		}
		c.mu.Lock()
		stale := call.version != c.version[keyID]
		if call.err == nil && !stale {
			set := make(map[int64]struct{}, len(call.interfaces))
			for _, candidate := range call.interfaces {
				set[candidate] = struct{}{}
			}
			c.items[keyID] = keyInterfaceCacheEntry{
				interfaces: set,
				expiresAt:  c.clock().Add(c.ttl),
			}
		}
		if c.loading[keyID] == call {
			delete(c.loading, keyID)
		}
		close(call.done)
		c.mu.Unlock()
		if panicValue != nil {
			panic(panicValue)
		}
		if stale {
			if err := ctx.Err(); err != nil {
				return false, err
			}
			continue
		}
		if call.err != nil {
			return false, call.err
		}
		for _, candidate := range call.interfaces {
			if candidate == interfaceID {
				return true, nil
			}
		}
		return false, nil
	}
}

func (s *Service) keyAuthByHash(ctx context.Context, hash string) (KeyAuth, error) {
	if s.keyAuthCache == nil {
		return s.repo.KeyAuthByHash(ctx, hash)
	}
	return s.keyAuthCache.getOrLoad(ctx, s.now(), hash, func() (KeyAuth, error) {
		return s.repo.KeyAuthByHash(ctx, hash)
	})
}

func (s *Service) keyAllowsInterface(ctx context.Context, keyID, interfaceID int64) (bool, error) {
	if s.keyInterfaceCache == nil {
		return s.repo.KeyAllowsInterface(ctx, keyID, interfaceID)
	}
	return s.keyInterfaceCache.allows(ctx, s.now(), keyID, interfaceID, func() ([]int64, error) {
		return s.repo.Interfaces(ctx, keyID)
	})
}

func (s *Service) invalidateKeyCaches(keyID int64) {
	s.keyAuthCache.invalidateKey(keyID)
	s.keyInterfaceCache.invalidateKey(keyID)
}
