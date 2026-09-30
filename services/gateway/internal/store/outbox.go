package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Gergov00/pricescount/shared/pkg/outbox"
	"github.com/jackc/pgx/v5"
)

// Claim leases unpublished gateway events for delivery.
func (s *Store) Claim(ctx context.Context, limit int, lease time.Duration) ([]outbox.Event, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer rollbackTx(ctx, tx)
	rows, err := tx.Query(ctx, `WITH picked AS (
	 SELECT id FROM gateway_outbox WHERE published_at IS NULL AND available_at<=NOW()
	 AND (locked_until IS NULL OR locked_until<NOW()) ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT $1
	) UPDATE gateway_outbox o SET lease_token=gen_random_uuid(), locked_until=NOW()+$2::interval, attempts=attempts+1
	FROM picked WHERE o.id=picked.id RETURNING o.id, o.lease_token, o.queue, o.payload, o.attempts`, limit, fmt.Sprintf("%f seconds", lease.Seconds()))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []outbox.Event
	for rows.Next() {
		var e outbox.Event
		if err := rows.Scan(&e.ID, &e.LeaseToken, &e.Queue, &e.Payload, &e.Attempts); err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return events, nil
}

// MarkPublished marks an event only while the caller still owns its lease.
func (s *Store) MarkPublished(ctx context.Context, id, leaseToken string) error {
	_, err := s.db.Exec(ctx, `UPDATE gateway_outbox SET published_at=NOW(), lease_token=NULL, locked_until=NULL, last_error=NULL WHERE id=$1 AND lease_token=$2 AND published_at IS NULL`, id, leaseToken)
	return err
}

// Retry releases an owned event with a bounded delivery delay.
func (s *Store) Retry(ctx context.Context, id, leaseToken, lastError string, delay time.Duration) error {
	_, err := s.db.Exec(ctx, `UPDATE gateway_outbox SET available_at=NOW()+$3::interval, lease_token=NULL, locked_until=NULL, last_error=$4 WHERE id=$1 AND lease_token=$2 AND published_at IS NULL`, id, leaseToken, fmt.Sprintf("%f seconds", delay.Seconds()), lastError)
	return err
}

func enqueue(ctx context.Context, tx pgx.Tx, key, queue string, payload any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO gateway_outbox(event_key,queue,payload) VALUES($1,$2,$3) ON CONFLICT(event_key) DO NOTHING`, key, queue, b)
	return err
}

func (s *Store) enqueue(ctx context.Context, tx pgx.Tx, key, queue string, payload any) error {
	return enqueue(ctx, tx, key, queue, payload)
}

// rollbackTx releases an uncommitted transaction. ErrTxClosed after Commit is expected and ignored.
func rollbackTx(ctx context.Context, tx pgx.Tx) { _ = tx.Rollback(ctx) }
