package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"modernc.org/sqlite"
)

func TestRevocationRoundTrip(t *testing.T) {
	t.Setenv("TUBO_HOME", t.TempDir())
	state, err := Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	if err := state.Revoke(context.Background(), "solar/default", "connect.access", "jti_access", 1); err != nil {
		t.Fatal(err)
	}
	revoked, err := state.IsRevoked(context.Background(), "solar/default", "connect.access", "jti_access")
	if err != nil || !revoked {
		t.Fatalf("revoked=%v err=%v", revoked, err)
	}
	if err := state.Revoke(context.Background(), "solar/default", "connect.access", "jti_access", 0); err == nil {
		t.Fatal("accepted zero epoch")
	}
}

const controlledRedeemDriverName = "tubo-controlled-redeem-test"

var (
	controlledRedeemDriver   = &redeemDriver{base: &sqlite.Driver{}}
	registerControlledDriver sync.Once
)

// redeemDriver is test-only. It pauses each worker at its first redemption
// statement: old code pauses after SELECT reaches EOF, while insert-first code
// pauses after INSERT completes. No production Store behavior is hooked.
type redeemDriver struct {
	base driver.Driver
	mu   sync.Mutex
	hook func()
}

func (d *redeemDriver) Open(name string) (driver.Conn, error) {
	conn, err := d.base.Open(name)
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	hook := d.hook
	d.mu.Unlock()
	return &redeemConn{Conn: conn, hook: hook}, nil
}

func (d *redeemDriver) setHook(hook func()) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.hook = hook
}

type redeemConn struct {
	driver.Conn
	hook func()
}

func (c *redeemConn) BeginTx(ctx context.Context, options driver.TxOptions) (driver.Tx, error) {
	return c.Conn.(driver.ConnBeginTx).BeginTx(ctx, options)
}

func (c *redeemConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	execer := c.Conn.(driver.ExecerContext)
	result, err := execer.ExecContext(ctx, query, args)
	if err == nil && strings.HasPrefix(strings.TrimSpace(query), "INSERT INTO invite_redemptions") && c.hook != nil {
		c.hook()
	}
	return result, err
}

func (c *redeemConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	queryer := c.Conn.(driver.QueryerContext)
	rows, err := queryer.QueryContext(ctx, query, args)
	if err != nil || !strings.HasPrefix(strings.TrimSpace(query), "SELECT client_public FROM invite_redemptions") || c.hook == nil {
		return rows, err
	}
	return &redeemRows{Rows: rows, hook: c.hook}, nil
}

type redeemRows struct {
	driver.Rows
	hook func()
	once sync.Once
}

func (r *redeemRows) Next(dest []driver.Value) error {
	err := r.Rows.Next(dest)
	if errors.Is(err, io.EOF) {
		r.once.Do(r.hook)
	}
	return err
}

func TestRedeemInviteControlledConcurrentRedemption(t *testing.T) {
	t.Setenv("TUBO_HOME", t.TempDir())
	state, err := Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()

	const workers = 8
	if err := state.db.Close(); err != nil {
		t.Fatal(err)
	}
	registerControlledDriver.Do(func() { sql.Register(controlledRedeemDriverName, controlledRedeemDriver) })
	arrived := make(chan struct{}, workers)
	release := make(chan struct{})
	var statements atomic.Int32
	controlledRedeemDriver.setHook(func() {
		if statements.Add(1) <= workers {
			arrived <- struct{}{}
			<-release
		}
	})
	t.Cleanup(func() { controlledRedeemDriver.setHook(nil) })
	state.db, err = sql.Open(controlledRedeemDriverName, "file:"+filepath.Join(state.home, "state.db")+"?_txlock=immediate&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	state.db.SetMaxOpenConns(workers)

	ready := sync.WaitGroup{}
	ready.Add(workers)
	start := make(chan struct{})
	type result struct {
		created bool
		err     error
	}
	results := make(chan result, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		session := fmt.Sprintf("ses_%d", i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			ready.Done()
			<-start
			created, err := state.RedeemInvite(context.Background(), "jti_controlled", "pip_one", session, "client_one")
			results <- result{created: created, err: err}
		}()
	}
	ready.Wait()
	close(start)
	for i := 0; i < workers; i++ {
		<-arrived
	}
	close(release)
	wg.Wait()
	close(results)

	created, idempotent := 0, 0
	for value := range results {
		if value.err != nil {
			t.Errorf("controlled redemption: %v", value.err)
			continue
		}
		if value.created {
			created++
		} else {
			idempotent++
		}
	}
	if created != 1 || idempotent != workers-1 {
		t.Fatalf("created=%d idempotent=%d, want 1 and %d", created, idempotent, workers-1)
	}
	count, err := state.RedemptionCount(context.Background(), "jti_controlled", "pip_one")
	if err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
}

func TestRedeemInviteAtomicAndIdempotent(t *testing.T) {
	t.Setenv("TUBO_HOME", t.TempDir())
	state, err := Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()

	const workers = 16
	type result struct {
		session string
		created bool
		err     error
	}
	ready := sync.WaitGroup{}
	ready.Add(workers)
	start := make(chan struct{})
	results := make(chan result, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		session := fmt.Sprintf("ses_%d", i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			ready.Done()
			<-start
			created, err := state.RedeemInvite(context.Background(), "jti_one", "pip_one", session, "client_one")
			results <- result{session: session, created: created, err: err}
		}()
	}
	ready.Wait()
	close(start)
	wg.Wait()
	close(results)

	created, idempotent := 0, 0
	winnerSession := ""
	for value := range results {
		if value.err != nil {
			t.Errorf("same client redemption: %v", value.err)
			continue
		}
		if value.created {
			created++
			winnerSession = value.session
		} else {
			idempotent++
		}
	}
	if created != 1 || idempotent != workers-1 {
		t.Fatalf("created=%d idempotent=%d, want 1 and %d", created, idempotent, workers-1)
	}
	count, err := state.RedemptionCount(context.Background(), "jti_one", "pip_one")
	if err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	var session, client string
	if err := state.db.QueryRowContext(context.Background(), `SELECT session_id,client_public FROM invite_redemptions WHERE jti=? AND pipe_id=?`, "jti_one", "pip_one").Scan(&session, &client); err != nil {
		t.Fatal(err)
	}
	if session != winnerSession || client != "client_one" {
		t.Fatalf("stored session=%q client=%q, want session=%q client=%q", session, client, winnerSession, "client_one")
	}
	createdAgain, err := state.RedeemInvite(context.Background(), "jti_one", "pip_one", "ses_retry", "client_one")
	if err != nil || createdAgain {
		t.Fatalf("same client retry created=%v err=%v", createdAgain, err)
	}
	if err := state.db.QueryRowContext(context.Background(), `SELECT session_id,client_public FROM invite_redemptions WHERE jti=? AND pipe_id=?`, "jti_one", "pip_one").Scan(&session, &client); err != nil {
		t.Fatal(err)
	}
	if session != winnerSession || client != "client_one" {
		t.Fatalf("retry mutated session=%q client=%q", session, client)
	}
	if _, err := state.RedeemInvite(context.Background(), "jti_one", "pip_one", "ses_other", "client_two"); !errors.Is(err, ErrInviteRedeemed) {
		t.Fatalf("other client err=%v", err)
	}
}

func TestRedeemInviteAtomicCompetingClients(t *testing.T) {
	t.Setenv("TUBO_HOME", t.TempDir())
	state, err := Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()

	const workers = 16
	type result struct {
		session string
		client  string
		created bool
		err     error
	}
	ready := sync.WaitGroup{}
	ready.Add(workers)
	start := make(chan struct{})
	results := make(chan result, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		session := fmt.Sprintf("ses_%d", i)
		client := fmt.Sprintf("client_%d", i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			ready.Done()
			<-start
			created, err := state.RedeemInvite(context.Background(), "jti_competing", "pip_one", session, client)
			results <- result{session: session, client: client, created: created, err: err}
		}()
	}
	ready.Wait()
	close(start)
	wg.Wait()
	close(results)

	created, denied := 0, 0
	winnerSession, winnerClient := "", ""
	for value := range results {
		if value.created {
			if value.err != nil {
				t.Errorf("winner err=%v", value.err)
			}
			created++
			winnerSession, winnerClient = value.session, value.client
			continue
		}
		if !errors.Is(value.err, ErrInviteRedeemed) {
			t.Errorf("loser client=%q err=%v, want ErrInviteRedeemed", value.client, value.err)
			continue
		}
		denied++
	}
	if created != 1 || denied != workers-1 {
		t.Fatalf("created=%d denied=%d, want 1 and %d", created, denied, workers-1)
	}
	count, err := state.RedemptionCount(context.Background(), "jti_competing", "pip_one")
	if err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	var session, client string
	if err := state.db.QueryRowContext(context.Background(), `SELECT session_id,client_public FROM invite_redemptions WHERE jti=? AND pipe_id=?`, "jti_competing", "pip_one").Scan(&session, &client); err != nil {
		t.Fatal(err)
	}
	if session != winnerSession || client != winnerClient {
		t.Fatalf("stored session=%q client=%q, want session=%q client=%q", session, client, winnerSession, winnerClient)
	}
	createdAgain, err := state.RedeemInvite(context.Background(), "jti_competing", "pip_one", "ses_retry", winnerClient)
	if err != nil || createdAgain {
		t.Fatalf("winner retry created=%v err=%v", createdAgain, err)
	}
	if err := state.db.QueryRowContext(context.Background(), `SELECT session_id,client_public FROM invite_redemptions WHERE jti=? AND pipe_id=?`, "jti_competing", "pip_one").Scan(&session, &client); err != nil {
		t.Fatal(err)
	}
	if session != winnerSession || client != winnerClient {
		t.Fatalf("retry mutated session=%q client=%q", session, client)
	}
}
