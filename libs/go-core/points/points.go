// Package points implements the immutable point ledger: grants,
// reservations, settlement capped at the reserved amount, and releases.
// Every mutating helper runs inside the caller's pgx transaction.
package points

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/skillsignal/go-core/ids"
)

// ErrInsufficientPoints marks a reservation the balance cannot cover.
var ErrInsufficientPoints = errors.New("insufficient points")

// Reservation is an open or closed point hold.
type Reservation struct {
	ID        string
	AccountID string
	Amount    int
	State     string
}

// Tx is the subset of pgx.Tx used by the ledger helpers.
type Tx interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

func newID() string { return ids.New() }

// Grant adds points idempotently and returns the new balance.
func Grant(ctx context.Context, tx Tx, accountID string, amount int, reason, idempotencyKey string) (int, error) {
	if amount <= 0 {
		return 0, errors.New("grant amounts must be positive")
	}
	if _, err := tx.Exec(ctx, `SELECT id FROM point_account WHERE id = $1 FOR UPDATE`, accountID); err != nil {
		return 0, err
	}
	var existing string
	err := tx.QueryRow(ctx,
		`SELECT id FROM point_ledger_entry WHERE account_id = $1 AND idempotency_key = $2`,
		accountID, idempotencyKey).Scan(&existing)
	if err == nil {
		return Balance(ctx, tx, accountID)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO point_ledger_entry (id, account_id, amount, reason, idempotency_key)
		 VALUES ($1, $2, $3, $4, $5)`,
		newID(), accountID, amount, reason, idempotencyKey); err != nil {
		return 0, err
	}
	return Balance(ctx, tx, accountID)
}

// Reserve holds points idempotently, failing when the available balance is short.
func Reserve(ctx context.Context, tx Tx, accountID string, amount int, purpose, idempotencyKey string) (Reservation, error) {
	if amount <= 0 {
		return Reservation{}, errors.New("reservation amounts must be positive")
	}
	var existing Reservation
	err := tx.QueryRow(ctx,
		`SELECT id, account_id, amount, state FROM point_reservation
		 WHERE account_id = $1 AND idempotency_key = $2`,
		accountID, idempotencyKey).Scan(&existing.ID, &existing.AccountID, &existing.Amount, &existing.State)
	if err == nil {
		return existing, nil
	}
	if _, err := tx.Exec(ctx, `SELECT id FROM point_account WHERE id = $1 FOR UPDATE`, accountID); err != nil {
		return Reservation{}, err
	}
	available, err := Available(ctx, tx, accountID)
	if err != nil {
		return Reservation{}, err
	}
	if available < amount {
		return Reservation{}, ErrInsufficientPoints
	}
	now := time.Now().UTC()
	reservation := Reservation{ID: newID(), AccountID: accountID, Amount: amount, State: "reserved"}
	if _, err := tx.Exec(ctx,
		`INSERT INTO point_reservation (id, account_id, amount, state, purpose, idempotency_key, created_at, updated_at)
		 VALUES ($1, $2, $3, 'reserved', $4, $5, $6, $6)`,
		reservation.ID, accountID, amount, purpose, idempotencyKey, now); err != nil {
		return Reservation{}, err
	}
	return reservation, nil
}

// Settle charges at most the reserved amount and closes the hold.
// A hold that is no longer open is left untouched.
func Settle(ctx context.Context, tx Tx, reservationID string, chargedAmount int, reason string) error {
	var accountID string
	var amount int
	err := tx.QueryRow(ctx,
		`SELECT account_id, amount FROM point_reservation WHERE id = $1 AND state = 'reserved' FOR UPDATE`,
		reservationID).Scan(&accountID, &amount)
	if err != nil {
		return nil
	}
	charge := chargedAmount
	if charge > amount {
		charge = amount
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO point_ledger_entry (id, account_id, amount, reason, idempotency_key)
		 VALUES ($1, $2, $3, $4, $5) ON CONFLICT (account_id, idempotency_key) DO NOTHING`,
		newID(), accountID, -charge, reason, "settle:"+reservationID); err != nil {
		return err
	}
	_, err = tx.Exec(ctx,
		`UPDATE point_reservation SET state = 'settled', updated_at = $1 WHERE id = $2`,
		time.Now().UTC(), reservationID)
	return err
}

// Release returns an open hold to the balance; closed holds are untouched.
func Release(ctx context.Context, tx Tx, reservationID string) error {
	_, err := tx.Exec(ctx,
		`UPDATE point_reservation SET state = 'released', updated_at = $1
		 WHERE id = $2 AND state = 'reserved'`,
		time.Now().UTC(), reservationID)
	return err
}

// Balance sums the immutable ledger for an account.
func Balance(ctx context.Context, tx Tx, accountID string) (int, error) {
	var total *int64
	err := tx.QueryRow(ctx,
		`SELECT COALESCE(SUM(amount), 0) FROM point_ledger_entry WHERE account_id = $1`,
		accountID).Scan(&total)
	if err != nil {
		return 0, err
	}
	if total == nil {
		return 0, nil
	}
	return int(*total), nil
}

// Reserved sums open holds for an account.
func Reserved(ctx context.Context, tx Tx, accountID string) (int, error) {
	var total *int64
	err := tx.QueryRow(ctx,
		`SELECT COALESCE(SUM(amount), 0) FROM point_reservation WHERE account_id = $1 AND state = 'reserved'`,
		accountID).Scan(&total)
	if err != nil {
		return 0, err
	}
	if total == nil {
		return 0, nil
	}
	return int(*total), nil
}

// Available is the spendable balance net of open holds.
func Available(ctx context.Context, tx Tx, accountID string) (int, error) {
	balance, err := Balance(ctx, tx, accountID)
	if err != nil {
		return 0, err
	}
	reserved, err := Reserved(ctx, tx, accountID)
	if err != nil {
		return 0, err
	}
	return balance - reserved, nil
}

// EnsureUserAccount returns the point account for a user, creating it once.
func EnsureUserAccount(ctx context.Context, tx Tx, userID string) (string, error) {
	return ensureAccount(ctx, tx, "owner_user_id", userID)
}

// EnsureOrganizationAccount returns the point account for an organization.
func EnsureOrganizationAccount(ctx context.Context, tx Tx, organizationID string) (string, error) {
	return ensureAccount(ctx, tx, "organization_id", organizationID)
}

func ensureAccount(ctx context.Context, tx Tx, column, value string) (string, error) {
	var id string
	err := tx.QueryRow(ctx,
		`SELECT id FROM point_account WHERE `+column+` = $1`, value).Scan(&id)
	if err == nil {
		return id, nil
	}
	id = newID()
	var ownerUserID, organizationID *string
	if column == "owner_user_id" {
		ownerUserID = &value
	} else {
		organizationID = &value
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO point_account (id, owner_user_id, organization_id) VALUES ($1, $2, $3)
		 ON CONFLICT DO NOTHING`,
		id, ownerUserID, organizationID)
	if err != nil {
		return "", err
	}
	err = tx.QueryRow(ctx,
		`SELECT id FROM point_account WHERE `+column+` = $1`, value).Scan(&id)
	return id, err
}
