package rbac

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"bugpk-api/internal/config"
	"bugpk-api/internal/domain/auth"
	"bugpk-api/internal/pkg/auditprincipal"
	apperrors "bugpk-api/internal/pkg/errors"
	"bugpk-api/internal/pkg/testdb"
)

type identityFixture struct {
	service      *Service
	jwt          *auth.JWTService
	queries      atomic.Int32
	version      atomic.Int64
	status       atomic.Int64
	beforeLookup func(context.Context) error
}

func newIdentityFixture(t *testing.T) *identityFixture {
	t.Helper()
	f := &identityFixture{}
	f.version.Store(1)
	f.status.Store(1)
	db := testdb.OpenStub(t, testdb.SQLStub{Query: func(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
		id, ok := args[0].Value.(int64)
		if !ok {
			return nil, fmt.Errorf("invalid lookup id: %v", args)
		}
		switch {
		case strings.Contains(q, "from bp_user"):
			f.queries.Add(1)
			if f.beforeLookup != nil {
				if err := f.beforeLookup(ctx); err != nil {
					return nil, err
				}
			}
			return testdb.Rows([]driver.Value{id, fmt.Sprintf("user-%d", id), f.status.Load(), int64(0), "", "", f.version.Load()}), nil
		case strings.Contains(q, "from bp_role"):
			role := "user"
			if id == 42 {
				role = "admin"
			}
			return testdb.Rows([]driver.Value{role}), nil
		default:
			return nil, fmt.Errorf("unexpected identity query: %s", q)
		}
	}})
	var err error
	f.jwt, err = auth.NewJWTService(config.AuthConfig{Secret: strings.Repeat("r", 64), Issuer: "request-identity-test", AccessTokenTTL: time.Hour, RefreshTokenTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	f.service = NewService(NewRepository(db), f.jwt)
	return f
}
func (f *identityFixture) token(t *testing.T, id, version int64) string {
	t.Helper()
	token, err := f.jwt.CreateAccessToken(auth.User{ID: id, Username: fmt.Sprintf("user-%d", id), TokenVersion: int(version)}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return token
}
func requireAuthStatus(t *testing.T, err error, status int) {
	t.Helper()
	business, ok := apperrors.AsBusinessError(err)
	if !ok || business.StatusCode != status {
		t.Fatalf("authentication error=%v, want HTTP %d", err, status)
	}
}

func TestRequestIdentityConcurrentCallsUseOneLiveLookupAndIndependentRoles(t *testing.T) {
	f := newIdentityFixture(t)
	token := f.token(t, 42, 1)
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	f.beforeLookup = func(ctx context.Context) error {
		once.Do(func() { close(started) })
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	ctx := WithRequestAuthentication(context.Background())
	var wait sync.WaitGroup
	for range 24 {
		wait.Go(func() {
			p, err := f.service.Authenticate(ctx, token)
			if err != nil {
				t.Error(err)
				return
			}
			if p.ID != 42 || len(p.Roles) != 1 || p.Roles[0] != "admin" {
				t.Errorf("wrong principal: %+v", p)
				return
			}
			p.Roles[0] = "caller-mutated"
		})
	}
	<-started
	close(release)
	wait.Wait()
	p, err := f.service.Authenticate(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if f.queries.Load() != 1 || p.Roles[0] != "admin" {
		t.Fatalf("lookup count=%d roles=%v", f.queries.Load(), p.Roles)
	}
}

func TestRequestIdentityNeverCrossesCredentialsOrAuthorizers(t *testing.T) {
	f := newIdentityFixture(t)
	ctx := WithRequestAuthentication(context.Background())
	token42 := f.token(t, 42, 1)
	first, err := f.service.Authenticate(ctx, token42)
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.service.Authenticate(ctx, f.token(t, 43, 1))
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != 42 || second.ID != 43 || second.Roles[0] != "user" || f.queries.Load() != 2 {
		t.Fatalf("credential identity crossed: %+v %+v", first, second)
	}
	another := newIdentityFixture(t)
	another.status.Store(0)
	_, err = another.service.Authenticate(ctx, token42)
	requireAuthStatus(t, err, 403)
}

func TestRequestIdentityDoesNotSurviveNewRequestOrUnscopedCalls(t *testing.T) {
	f := newIdentityFixture(t)
	token := f.token(t, 42, 1)
	first := WithRequestAuthentication(context.Background())
	if _, err := f.service.Authenticate(first, token); err != nil {
		t.Fatal(err)
	}
	f.version.Store(2)
	_, err := f.service.Authenticate(WithRequestAuthentication(first), token)
	requireAuthStatus(t, err, 401)
	if f.queries.Load() != 2 {
		t.Fatal("child HTTP request inherited a stale principal")
	}
	f.version.Store(1)
	if _, err := f.service.Authenticate(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	f.status.Store(0)
	_, err = f.service.Authenticate(context.Background(), token)
	requireAuthStatus(t, err, 403)
}

// Layered middleware must reuse the scope an outer layer already opened. A
// second scope would resolve the credential again and drop the identity the
// audit layer recorded onto its own context.
func TestEnsureRequestAuthenticationReusesOuterScope(t *testing.T) {
	f := newIdentityFixture(t)
	token := f.token(t, 42, 1)
	outer := WithRequestAuthentication(context.Background())
	if _, err := f.service.Authenticate(outer, token); err != nil {
		t.Fatal(err)
	}
	inner := EnsureRequestAuthentication(outer)
	if inner != outer {
		t.Fatal("ensure replaced an existing request scope")
	}
	if _, err := f.service.Authenticate(inner, token); err != nil {
		t.Fatal(err)
	}
	if f.queries.Load() != 1 {
		t.Fatalf("layered middleware resolved the credential %d times", f.queries.Load())
	}
}

func TestEnsureRequestAuthenticationCreatesScopeWhenAbsent(t *testing.T) {
	f := newIdentityFixture(t)
	token := f.token(t, 42, 1)
	ctx := EnsureRequestAuthentication(context.Background())
	if _, err := f.service.Authenticate(ctx, token); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Authenticate(ctx, token); err != nil {
		t.Fatal(err)
	}
	if f.queries.Load() != 1 {
		t.Fatalf("scope was not established: queries=%d", f.queries.Load())
	}
}

// The explicit constructor keeps its documented meaning: it always isolates,
// which is what a genuine child request depends on.
func TestWithRequestAuthenticationStillStartsNewScope(t *testing.T) {
	f := newIdentityFixture(t)
	token := f.token(t, 42, 1)
	outer := EnsureRequestAuthentication(context.Background())
	if _, err := f.service.Authenticate(outer, token); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Authenticate(WithRequestAuthentication(outer), token); err != nil {
		t.Fatal(err)
	}
	if f.queries.Load() != 2 {
		t.Fatalf("new scope reused the parent identity: queries=%d", f.queries.Load())
	}
}

func TestRequestIdentityDoesNotCacheInfrastructureFailures(t *testing.T) {
	f := newIdentityFixture(t)
	token := f.token(t, 42, 1)
	ctx := WithRequestAuthentication(context.Background())
	f.beforeLookup = func(context.Context) error {
		if f.queries.Load() == 1 {
			return errors.New("database unavailable")
		}
		return nil
	}
	_, err := f.service.Authenticate(ctx, token)
	requireAuthStatus(t, err, 503)
	p, err := f.service.Authenticate(ctx, token)
	if err != nil || p.ID != 42 || f.queries.Load() != 2 {
		t.Fatalf("failed authentication was reused: p=%+v err=%v", p, err)
	}
}

func TestRequestIdentityCancelledWaiterDoesNotBlockBehindLookup(t *testing.T) {
	f := newIdentityFixture(t)
	token := f.token(t, 42, 1)
	started, release := make(chan struct{}), make(chan struct{})
	var startedOnce sync.Once
	f.beforeLookup = func(context.Context) error { startedOnce.Do(func() { close(started) }); <-release; return nil }
	ctx := WithRequestAuthentication(context.Background())
	first := make(chan error, 1)
	go func() { _, err := f.service.Authenticate(ctx, token); first <- err }()
	<-started
	cancelled, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	waiting := make(chan error, 1)
	go func() { _, err := f.service.Authenticate(cancelled, token); waiting <- err }()
	var err error
	select {
	case err = <-waiting:
	case <-time.After(time.Second):
		close(release)
		<-first
		t.Fatal("cancelled waiter remained blocked")
	}
	close(release)
	requireAuthStatus(t, err, 503)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if f.queries.Load() != 1 {
		t.Fatal("cancelled waiter initiated another lookup")
	}
	_, err = f.service.Authenticate(cancelled, token)
	requireAuthStatus(t, err, 503)
}

func TestContextAttributionCannotAuthorizeAndIsNotMutableThroughRoles(t *testing.T) {
	f := newIdentityFixture(t)
	p := Principal{ID: 999, Roles: []string{"admin"}}
	ctx := WithRequestAuthentication(WithPrincipal(context.Background(), p))
	p.Roles[0] = "modified"
	got, _ := PrincipalFromContext(ctx)
	if got.Roles[0] != "admin" {
		t.Fatal("stored attribution aliases caller roles")
	}
	got.Roles[0] = "modified-again"
	again, _ := PrincipalFromContext(ctx)
	if again.Roles[0] != "admin" {
		t.Fatal("returned attribution aliases stored roles")
	}
	_, err := f.service.Authenticate(ctx, "not-a-token")
	requireAuthStatus(t, err, 401)
	authenticated, err := f.service.Authenticate(ctx, f.token(t, 43, 1))
	if err != nil || authenticated.ID != 43 || authenticated.Roles[0] != "user" {
		t.Fatalf("context bypassed authentication: %+v %v", authenticated, err)
	}
}

func TestRequestIdentityStillValidatesJWTForEverySnapshotHit(t *testing.T) {
	f := newIdentityFixture(t)
	expired, err := f.jwt.CreateAccessToken(auth.User{ID: 42, Username: "user-42", TokenVersion: 1}, time.Now().Add(-2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	refresh, _, _, err := f.jwt.CreateRefreshToken(auth.User{ID: 42, Username: "user-42", TokenVersion: 1}, "device", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	other, err := auth.NewJWTService(config.AuthConfig{Secret: strings.Repeat("r", 64), Issuer: "other-issuer", AccessTokenTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	wrongIssuer, err := other.CreateAccessToken(auth.User{ID: 42, Username: "user-42", TokenVersion: 1}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	wrongKeyJWT, err := auth.NewJWTService(config.AuthConfig{Secret: strings.Repeat("x", 64), Issuer: "request-identity-test", AccessTokenTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	wrongSignature, err := wrongKeyJWT.CreateAccessToken(auth.User{ID: 42, Username: "user-42", TokenVersion: 1}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for name, token := range map[string]string{"expired": expired, "refresh": refresh, "issuer": wrongIssuer, "signature": wrongSignature, "malformed": "not.a.valid-signature"} {
		t.Run(name, func(t *testing.T) {
			ctx := WithRequestAuthentication(context.Background())
			// Model a previously populated snapshot whose credential is no longer valid.
			_, err := f.service.authenticateInRequest(ctx, token, func() (Principal, error) { return Principal{ID: 42, Roles: []string{"admin"}}, nil })
			if err != nil {
				t.Fatal(err)
			}
			_, err = f.service.Authenticate(ctx, token)
			requireAuthStatus(t, err, 401)
		})
	}
	if f.queries.Load() != 0 {
		t.Fatal("invalid JWT reached the identity store")
	}
}

// Done is evaluated only after the waiter has captured the existing entry.
// This handshake avoids a sleep-based guess that a goroutine is really waiting.
type authenticationWaitSignal struct {
	context.Context
	entered chan struct{}
	once    sync.Once
}

func (c *authenticationWaitSignal) Done() <-chan struct{} {
	c.once.Do(func() { close(c.entered) })
	return c.Context.Done()
}
func TestRequestIdentityPanicReleasesWaitersWithoutPublishingSuccess(t *testing.T) {
	f := newIdentityFixture(t)
	token := f.token(t, 42, 1)
	ctx, cancel := context.WithTimeout(WithRequestAuthentication(context.Background()), 2*time.Second)
	defer cancel()
	started, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	releaseLeader := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseLeader()
	leader := make(chan any, 1)
	go func() {
		defer func() { leader <- recover() }()
		_, _ = f.service.authenticateInRequest(ctx, token, func() (Principal, error) { close(started); <-release; panic("aborted authentication") })
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("leader did not start")
	}
	waiterCtx := &authenticationWaitSignal{Context: ctx, entered: make(chan struct{})}
	type result struct {
		principal Principal
		err       error
	}
	waiter := make(chan result, 1)
	go func() { p, err := f.service.Authenticate(waiterCtx, token); waiter <- result{p, err} }()
	select {
	case <-waiterCtx.entered:
	case <-ctx.Done():
		t.Fatal("waiter did not capture the old entry")
	}
	releaseLeader()
	if recovered := <-leader; recovered == nil {
		t.Fatal("leader did not panic")
	}
	select {
	case got := <-waiter:
		if got.err != nil || got.principal.ID != 42 {
			t.Fatalf("waiter was not released to retry: %+v %v", got.principal, got.err)
		}
	case <-ctx.Done():
		t.Fatal("panic left an existing waiter blocked")
	}
}

func TestAuditCaptureIsReadOnlyAndCannotBypassAuthentication(t *testing.T) {
	f := newIdentityFixture(t)
	token := f.token(t, 43, 1)
	ctx := auditprincipal.WithRequest(WithRequestAuthentication(context.Background()), token)
	auditprincipal.Record(ctx, token, auditprincipal.Identity{UserID: 999, Username: "forged", Roles: []string{"admin"}})
	p, err := f.service.Authenticate(ctx, token)
	if err != nil || p.ID != 43 || p.Roles[0] != "user" {
		t.Fatalf("audit metadata authorized an operation: %+v %v", p, err)
	}
	live := auditprincipal.WithRequest(WithRequestAuthentication(context.Background()), token)
	if _, ok := auditprincipal.FromContext(live); ok {
		t.Fatal("new request inherited attribution")
	}
	if _, err := f.service.Authenticate(live, token); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(live)
	cancel()
	identity, ok := auditprincipal.FromContext(cancelled)
	if !ok || identity.UserID != 43 {
		t.Fatal("completed attribution lost after cancellation")
	}
	_, err = f.service.Authenticate(cancelled, token)
	requireAuthStatus(t, err, 503)
}
