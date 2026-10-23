// Worker billing: completion settlement and hold releases.
package pipeline

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/skillsignal/backend/billing"
	"github.com/skillsignal/backend/config"
	"github.com/skillsignal/backend/ids"
)

// settlePointsFromUsage charges the greater of the minimum or the
// ceiling-rounded reported cost. Zero provider cost still incurs the minimum;
// price ceilings bound the accepted quote rather than estimate settlement.
func settlePointsFromUsage(promptTokens, completionTokens int, reportedCostUSD float64, kind string, settings config.BillingSettings) int {
	billingSettings := billingSettingsFrom(settings)
	return billing.SettleFromUsage(promptTokens, completionTokens, reportedCostUSD, kind, billingSettings)
}

func billingSettingsFrom(settings config.BillingSettings) billing.Settings {
	converted := billing.DefaultSettings()
	converted.PointsPerUSD = settings.PointsPerUSD
	converted.MinIndependentPoints = settings.MinIndependentPoints
	converted.MinEmployerResumePoints = settings.MinEmployerResumePoints
	converted.PriceCeilingInputPerMil = settings.PriceCeilingInputPerMil
	converted.PriceCeilingOutputPerMil = settings.PriceCeilingOutputPerMil
	return converted
}

// settleReservation charges at most the reserved amount and closes the
// hold. A hold that is no longer open is left untouched.
func settleReservation(ctx context.Context, tx pgx.Tx, reservationID string, chargedPoints int, reason string) error {
	var accountID string
	var amount int
	err := tx.QueryRow(ctx,
		`SELECT account_id, amount FROM point_reservation WHERE id = $1 AND state = 'reserved' FOR UPDATE`,
		reservationID).Scan(&accountID, &amount)
	if err == pgx.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	charge := chargedPoints
	if charge > amount {
		charge = amount
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO point_ledger_entry (id, account_id, amount, reason, idempotency_key)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (account_id, idempotency_key) DO NOTHING`,
		ids.New(), accountID, -charge, reason, "settle:"+reservationID); err != nil {
		return err
	}
	_, err = tx.Exec(ctx,
		`UPDATE point_reservation SET state = 'settled', updated_at = now() WHERE id = $1`,
		reservationID)
	return err
}

// releaseEvaluationReservations frees unsettled holds for evaluations.
func releaseEvaluationReservations(ctx context.Context, tx pgx.Tx, clause string, args []any) error {
	_, err := tx.Exec(ctx,
		`UPDATE point_reservation SET state = 'released', updated_at = now()
		 WHERE state = 'reserved'
		   AND id IN (
		       SELECT point_reservation_id FROM evaluation
		       WHERE point_reservation_id IS NOT NULL AND `+clause+`)`,
		args...)
	return err
}
