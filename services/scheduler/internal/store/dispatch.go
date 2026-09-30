package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/Gergov00/pricescount/shared/pkg/broker"
	"github.com/Gergov00/pricescount/shared/pkg/contracts"
	platformutil "github.com/Gergov00/pricescount/shared/pkg/platform"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const dueBatchSize = 100

func validWBIdentity(rawURL, platform string) bool {
	u, err := url.Parse(rawURL)
	if err != nil || platform != "wb" || u.Scheme != "https" || (u.Host != "www.wildberries.ru" && u.Host != "wildberries.ru") {
		return false
	}
	normalized, err := platformutil.NormalizeWB(rawURL)
	return err == nil && normalized == rawURL
}

// ApplySnapshot applies a strictly newer product monitoring state and keeps inactive tombstones.
func (s *Store) ApplySnapshot(ctx context.Context, request contracts.TrackRequest) error {
	if request.Action != "set_state" || request.Version <= 0 || request.IntervalHours <= 0 {
		return fmt.Errorf("snapshot fields: %w", ErrInvalidRequest)
	}
	productID, err := uuid.Parse(request.ProductID)
	if err != nil {
		return fmt.Errorf("snapshot product ID: %w", ErrInvalidRequest)
	}
	if _, err := uuid.Parse(request.TaskID); err != nil {
		return fmt.Errorf("snapshot task ID: %w", ErrInvalidRequest)
	}
	if !validWBIdentity(request.URL, request.Platform) {
		return fmt.Errorf("snapshot URL/platform: %w", ErrInvalidRequest)
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin monitoring snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, productID.String()); err != nil {
		return fmt.Errorf("lock snapshot product: %w", err)
	}
	var existingURL string
	err = tx.QueryRow(ctx, `SELECT url FROM scheduled_urls WHERE product_id=$1 FOR UPDATE`, productID).Scan(&existingURL)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("lookup snapshot product: %w", err)
	}
	if err == nil && existingURL != request.URL {
		return fmt.Errorf("snapshot product/URL mismatch: %w", ErrInvalidRequest)
	}
	tag, err := tx.Exec(ctx,
		`INSERT INTO scheduled_urls(product_id,url,platform,next_check_at,check_interval_hours,active,monitor_version)
		 VALUES($1,$2,$3,NOW(),$4,$5,$6)
		 ON CONFLICT(url) DO UPDATE SET platform=EXCLUDED.platform,
		   check_interval_hours=EXCLUDED.check_interval_hours,active=EXCLUDED.active,monitor_version=EXCLUDED.monitor_version
		 WHERE scheduled_urls.monitor_version < EXCLUDED.monitor_version
		   AND scheduled_urls.product_id=EXCLUDED.product_id`,
		productID, request.URL, request.Platform, request.IntervalHours, request.Active, request.Version)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return fmt.Errorf("snapshot product/URL conflict: %w", ErrInvalidRequest)
		}
		return fmt.Errorf("apply monitoring snapshot: %w", err)
	}
	if tag.RowsAffected() == 0 {
		var existingProduct string
		err = tx.QueryRow(ctx, `SELECT product_id::text FROM scheduled_urls WHERE url=$1`, request.URL).Scan(&existingProduct)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("snapshot URL mismatch: %w", ErrInvalidRequest)
		}
		if err != nil {
			return fmt.Errorf("check snapshot URL: %w", err)
		}
		if existingProduct != productID.String() {
			return fmt.Errorf("snapshot URL/product mismatch: %w", ErrInvalidRequest)
		}
	}
	return tx.Commit(ctx)
}

// EnqueueDue atomically advances a bounded batch and records scraper tasks in the outbox.
func (s *Store) EnqueueDue(ctx context.Context) (int, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin due enqueue: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx,
		`SELECT id,product_id,url,platform,check_interval_hours FROM scheduled_urls
		 WHERE active AND next_check_at<=NOW() ORDER BY next_check_at,id FOR UPDATE SKIP LOCKED LIMIT $1`, dueBatchSize)
	if err != nil {
		return 0, fmt.Errorf("select due URLs: %w", err)
	}
	type due struct {
		id, productID, url, platform string
		interval                     int
	}
	var entries []due
	for rows.Next() {
		var d due
		if err := rows.Scan(&d.id, &d.productID, &d.url, &d.platform, &d.interval); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan due URL: %w", err)
		}
		entries = append(entries, d)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, fmt.Errorf("read due URLs: %w", err)
	}
	rows.Close()
	for _, d := range entries {
		task := contracts.ScraperTask{TaskID: uuid.NewString(), ProductID: d.productID, URL: d.url, Platform: d.platform, ScheduledAt: time.Now().UTC()}
		payload, err := json.Marshal(task)
		if err != nil {
			return 0, fmt.Errorf("encode scraper task: %w", err)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO scheduler_outbox(event_key,queue,payload) VALUES($1,$2,$3)`, "periodic:"+task.TaskID, broker.QueueScraperTasks, payload); err != nil {
			return 0, fmt.Errorf("enqueue scraper task: %w", err)
		}
		if _, err = tx.Exec(ctx, `UPDATE scheduled_urls SET next_check_at=NOW()+(check_interval_hours*INTERVAL '1 hour') WHERE id=$1`, d.id); err != nil {
			return 0, fmt.Errorf("advance scheduled URL: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit due enqueue: %w", err)
	}
	return len(entries), nil
}

// EnqueueForce stores a one-shot scraper task once per stable Gateway TaskID without touching schedule state.
func (s *Store) EnqueueForce(ctx context.Context, request contracts.TrackRequest) error {
	if request.Action != "force" || request.ProductID == "" || !validWBIdentity(request.URL, request.Platform) {
		return fmt.Errorf("force fields: %w", ErrInvalidRequest)
	}
	productID, err := uuid.Parse(request.ProductID)
	if err != nil {
		return fmt.Errorf("force product ID: %w", ErrInvalidRequest)
	}
	taskID, err := uuid.Parse(request.TaskID)
	if err != nil {
		return fmt.Errorf("force task ID: %w", ErrInvalidRequest)
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin force enqueue: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var actualProduct string
	err = tx.QueryRow(ctx, `SELECT product_id::text FROM scheduled_urls WHERE url=$1 FOR SHARE`, request.URL).Scan(&actualProduct)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("lookup force URL: %w", err)
	}
	if err == nil && actualProduct != productID.String() {
		return fmt.Errorf("force URL/product mismatch: %w", ErrInvalidRequest)
	}
	tag, err := tx.Exec(ctx, `INSERT INTO processed_force_commands(task_id,product_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, taskID, productID)
	if err != nil {
		return fmt.Errorf("record force command: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return tx.Commit(ctx)
	}
	task := contracts.ScraperTask{TaskID: taskID.String(), ProductID: productID.String(), URL: request.URL, Platform: request.Platform, ScheduledAt: time.Now().UTC(), Force: true, ChatID: request.ChatID}
	payload, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("encode force task: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO scheduler_outbox(event_key,queue,payload) VALUES($1,$2,$3)`, "force:"+taskID.String(), broker.QueueScraperTasks, payload); err != nil {
		return fmt.Errorf("enqueue force task: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit force enqueue: %w", err)
	}
	return nil
}
