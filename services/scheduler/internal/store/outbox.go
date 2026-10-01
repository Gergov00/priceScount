package store

import (
	"context"
	"fmt"
	"time"

	"github.com/Gergov00/pricescount/shared/pkg/outbox"
	"github.com/jackc/pgx/v5"
)

// Claim leases pending Scheduler events in a short transaction.
func (s *Store) Claim(ctx context.Context, limit int, lease time.Duration) ([]outbox.Event, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin outbox claim: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx,
		`WITH picked AS (
		 SELECT id FROM scheduler_outbox WHERE published_at IS NULL AND available_at<=NOW()
		 AND (locked_until IS NULL OR locked_until<=NOW()) ORDER BY created_at,id
		 FOR UPDATE SKIP LOCKED LIMIT $1
		)
		UPDATE scheduler_outbox o SET lease_token=gen_random_uuid(),locked_until=NOW()+$2::interval,attempts=o.attempts+1
		FROM picked WHERE o.id=picked.id
		RETURNING o.id::text,o.lease_token::text,o.queue,o.payload,o.attempts`, limit, lease.String())
	if err != nil {
		return nil, fmt.Errorf("claim scheduler outbox: %w", err)
	}
	var events []outbox.Event
	for rows.Next() {
		var e outbox.Event
		if err := rows.Scan(&e.ID, &e.LeaseToken, &e.Queue, &e.Payload, &e.Attempts); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan scheduler outbox: %w", err)
		}
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("read scheduler outbox: %w", err)
	}
	rows.Close()
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit outbox claim: %w", err)
	}
	return events, nil
}

// MarkPublished completes an event only while the caller owns its lease.
func (s *Store) MarkPublished(ctx context.Context, id, token string) error {
	tag, err := s.db.Exec(ctx, `UPDATE scheduler_outbox SET published_at=NOW(),lease_token=NULL,locked_until=NULL WHERE id=$1 AND lease_token=$2 AND published_at IS NULL`, id, token)
	if err != nil {
		return fmt.Errorf("mark scheduler event published: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// Retry makes a leased event available again and records the most recent failure.
func (s *Store) Retry(ctx context.Context, id, token, lastError string, delay time.Duration) error {
	tag, err := s.db.Exec(ctx, `UPDATE scheduler_outbox SET available_at=NOW()+$3::interval,lease_token=NULL,locked_until=NULL,last_error=$4 WHERE id=$1 AND lease_token=$2 AND published_at IS NULL`, id, token, delay.String(), lastError)
	if err != nil {
		return fmt.Errorf("retry scheduler event: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}
