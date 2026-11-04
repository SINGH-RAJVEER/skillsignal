package points

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/skillsignal/backend/ids"
)

// Use a migrated disposable database; each test owns and removes its account.
func TestConcurrentReservationAndSettlement(t *testing.T) {
	address := os.Getenv("SKILLSIGNAL_TEST_DATABASE_URL")
	if address == "" {
		t.Skip("SKILLSIGNAL_TEST_DATABASE_URL is not configured")
	}
	ctx := context.Background()
	configuration, err := pgxpool.ParseConfig(address)
	if err != nil {
		t.Fatal(err)
	}
	configuration.ConnConfig.RuntimeParams["search_path"] = "billing,identity,public"
	pool, err := pgxpool.NewWithConfig(ctx, configuration)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	userID, accountID := ids.New(), ids.New()
	if _, err := pool.Exec(ctx, `INSERT INTO identity."user"(id,name,email,account_type) VALUES($1,'Ledger test',$2,'candidate')`, userID, userID+"@example.test"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := pool.Exec(ctx, `DELETE FROM identity."user" WHERE id=$1`, userID); err != nil {
			t.Error(err)
		}
	}()
	if _, err := pool.Exec(ctx, `INSERT INTO point_account(id,owner_user_id) VALUES($1,$2)`, accountID, userID); err != nil {
		t.Fatal(err)
	}
	if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		_, err := Grant(ctx, tx, accountID, 100, "test", "initial")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	results := make(chan Reservation, 8)
	failures := make(chan error, 8)
	for range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			var hold Reservation
			err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
				var err error
				hold, err = Reserve(ctx, tx, accountID, 80, "test", "same-operation")
				return err
			})
			if err != nil {
				failures <- err
				return
			}
			results <- hold
		}()
	}
	workers.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	if t.Failed() {
		t.FailNow()
	}
	close(results)
	var holdID string
	for hold := range results {
		if holdID != "" && hold.ID != holdID {
			t.Fatal("concurrent retries created different holds")
		}
		holdID = hold.ID
	}
	if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		_, err := Reserve(ctx, tx, accountID, 30, "test", "overdraw")
		if !errors.Is(err, ErrInsufficientPoints) {
			t.Fatalf("overdraw error: %v", err)
		}
		if err := Settle(ctx, tx, holdID, -1, "invalid"); err == nil {
			t.Fatal("negative charge accepted")
		}
		if err := Settle(ctx, tx, holdID, 200, "capped"); err != nil {
			return err
		}
		if err := Settle(ctx, tx, holdID, 80, "retry"); err != nil {
			return err
		}
		balance, err := Available(ctx, tx, accountID)
		if err == nil && balance != 20 {
			t.Fatalf("available points = %d, want 20", balance)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
