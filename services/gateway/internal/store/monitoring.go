package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/Gergov00/pricescount/shared/pkg/broker"
	"github.com/Gergov00/pricescount/shared/pkg/contracts"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (s *Store) CreateLookup(ctx context.Context, id, url string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollbackTx(ctx, tx)
	if _, err = tx.Exec(ctx, `INSERT INTO lookup_requests(id,url) VALUES($1,$2)`, id, url); err != nil {
		return fmt.Errorf("create lookup: %w", err)
	}
	task := contracts.LookupTask{TaskID: uuid.NewString(), LookupID: id, URL: url, Platform: "wb"}
	if err = s.enqueue(ctx, tx, "lookup:"+task.TaskID, broker.QueueLookupTasks, task); err != nil {
		return fmt.Errorf("queue lookup: %w", err)
	}
	return tx.Commit(ctx)
}

func (s *Store) lockProduct(ctx context.Context, tx pgx.Tx, productID string) (int64, string, string, error) {
	var version int64
	var url, platform string
	err := tx.QueryRow(ctx, `SELECT monitor_version,url,platform FROM products WHERE id=$1 FOR UPDATE`, productID).Scan(&version, &url, &platform)
	return version, url, platform, err
}

func (s *Store) snapshot(ctx context.Context, tx pgx.Tx, productID string, version int64, url, platform string) error {
	var active bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM subscriptions WHERE product_id=$1 AND active AND NOT paused)`, productID).Scan(&active); err != nil {
		return err
	}
	request := contracts.TrackRequest{TaskID: uuid.NewString(), Action: "set_state", ProductID: productID, URL: url, Platform: platform, Version: version, Active: active, IntervalHours: 1}
	return s.enqueue(ctx, tx, fmt.Sprintf("track:%s:%d", productID, version), broker.QueueTrackRequests, request)
}

func (s *Store) mutateSubscription(ctx context.Context, subID, userID, operation string, minPrice, maxPrice float64) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollbackTx(ctx, tx)
	var productID string
	var isActive bool
	err = tx.QueryRow(ctx, `SELECT product_id,active FROM subscriptions WHERE id=$1 AND user_id=$2`, subID, userID).Scan(&productID, &isActive)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("subscription %s: %w", subID, ErrNotFound)
	}
	if err != nil {
		return err
	}
	if operation != "delete" && !isActive {
		return fmt.Errorf("subscription %s: %w", subID, ErrNotFound)
	}
	version, url, platform, err := s.lockProduct(ctx, tx, productID)
	if err != nil {
		return err
	}
	if err = tx.QueryRow(ctx, `SELECT active FROM subscriptions WHERE id=$1 AND user_id=$2 FOR UPDATE`, subID, userID).Scan(&isActive); errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("subscription %s: %w", subID, ErrNotFound)
	} else if err != nil {
		return err
	}
	if operation != "delete" && !isActive {
		return fmt.Errorf("subscription %s: %w", subID, ErrNotFound)
	}
	switch operation {
	case "pause":
		_, err = tx.Exec(ctx, `UPDATE subscriptions SET paused=true WHERE id=$1 AND user_id=$2 AND active`, subID, userID)
	case "resume":
		_, err = tx.Exec(ctx, `UPDATE subscriptions SET paused=false WHERE id=$1 AND user_id=$2 AND active`, subID, userID)
	case "thresholds":
		_, err = tx.Exec(ctx, `UPDATE subscriptions SET min_price=$3,max_price=$4,alert_state='',alert_version=alert_version+1 WHERE id=$1 AND user_id=$2 AND (min_price IS DISTINCT FROM $3 OR max_price IS DISTINCT FROM $4)`, subID, userID, minPrice, maxPrice)
	case "delete":
		var owner string
		var active bool
		lookupErr := tx.QueryRow(ctx, `SELECT user_id,active FROM subscriptions WHERE id=$1`, subID).Scan(&owner, &active)
		if errors.Is(lookupErr, pgx.ErrNoRows) {
			return fmt.Errorf("subscription %s: %w", subID, ErrNotFound)
		}
		if lookupErr != nil {
			return lookupErr
		}
		if owner != userID {
			return fmt.Errorf("subscription %s: %w", subID, ErrNotFound)
		}
		if !active {
			return nil
		}
		_, err = tx.Exec(ctx, `UPDATE subscriptions SET active=false WHERE id=$1`, subID)
	}
	if err != nil {
		return err
	}
	// A snapshot is emitted for every state mutation. Product row locking gives it a strictly ordered version.
	version++
	if _, err = tx.Exec(ctx, `UPDATE products SET monitor_version=$2 WHERE id=$1`, productID, version); err != nil {
		return err
	}
	if err = s.snapshot(ctx, tx, productID, version, url, platform); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) PauseSubscription(ctx context.Context, subID, userID string) error {
	return s.mutateSubscription(ctx, subID, userID, "pause", 0, 0)
}
func (s *Store) ResumeSubscription(ctx context.Context, subID, userID string) error {
	return s.mutateSubscription(ctx, subID, userID, "resume", 0, 0)
}
func (s *Store) UpdateThresholds(ctx context.Context, subID, userID string, minPrice, maxPrice float64) error {
	return s.mutateSubscription(ctx, subID, userID, "thresholds", minPrice, maxPrice)
}
func (s *Store) DeleteSubscription(ctx context.Context, subID, userID string) error {
	return s.mutateSubscription(ctx, subID, userID, "delete", 0, 0)
}

func (s *Store) CreateSubscription(ctx context.Context, userID, productID string, minPrice, maxPrice float64) (string, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer rollbackTx(ctx, tx)
	version, url, platform, err := s.lockProduct(ctx, tx, productID)
	if err != nil {
		return "", err
	}
	var id string
	var oldMin, oldMax *float64
	var wasActive, wasPaused bool
	err = tx.QueryRow(ctx, `SELECT id,min_price,max_price,active,paused FROM subscriptions WHERE user_id=$1 AND product_id=$2 FOR UPDATE`, userID, productID).Scan(&id, &oldMin, &oldMax, &wasActive, &wasPaused)
	changed := false
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, `INSERT INTO subscriptions(user_id,product_id,min_price,max_price) VALUES($1,$2,$3,$4) RETURNING id`, userID, productID, minPrice, maxPrice).Scan(&id)
		changed = true
	} else if err == nil {
		resetAlert := !wasActive || oldMin == nil || *oldMin != minPrice || oldMax == nil || *oldMax != maxPrice
		changed = resetAlert || wasPaused
		_, err = tx.Exec(ctx, `UPDATE subscriptions SET min_price=$2,max_price=$3,active=true,paused=false,alert_state=CASE WHEN $4 THEN '' ELSE alert_state END,alert_version=alert_version+CASE WHEN $4 THEN 1 ELSE 0 END WHERE id=$1`, id, minPrice, maxPrice, resetAlert)
	}
	if err != nil {
		return "", fmt.Errorf("create subscription: %w", err)
	}
	if changed {
		version++
		if _, err = tx.Exec(ctx, `UPDATE products SET monitor_version=$2 WHERE id=$1`, productID, version); err != nil {
			return "", err
		}
		if err = s.snapshot(ctx, tx, productID, version, url, platform); err != nil {
			return "", err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return "", err
	}
	return id, nil
}

// QueueForce saves an authorized one-shot check and returns its stable TaskID.
func (s *Store) QueueForce(ctx context.Context, subID, userID string, chatID int64) (string, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer rollbackTx(ctx, tx)
	var productID, url, platform string
	var ownerChat int64
	err = tx.QueryRow(ctx, `SELECT p.id,p.url,p.platform,u.chat_id FROM subscriptions s JOIN products p ON p.id=s.product_id JOIN users u ON u.id=s.user_id WHERE s.id=$1 AND s.user_id=$2 AND s.active FOR UPDATE OF p`, subID, userID).Scan(&productID, &url, &platform, &ownerChat)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("subscription: %w", ErrNotFound)
	}
	if err != nil {
		return "", err
	}
	if ownerChat != chatID {
		return "", fmt.Errorf("force requester mismatch: %w", ErrNotFound)
	}
	taskID := uuid.NewString()
	if _, err = tx.Exec(ctx, `INSERT INTO force_requests(task_id,product_id,user_id,chat_id) VALUES($1,$2,$3,$4)`, taskID, productID, userID, chatID); err != nil {
		return "", err
	}
	request := contracts.TrackRequest{TaskID: taskID, Action: "force", ProductID: productID, URL: url, Platform: platform, ChatID: chatID}
	if err = s.enqueue(ctx, tx, "force:"+taskID, broker.QueueTrackRequests, request); err != nil {
		return "", err
	}
	if err = tx.Commit(ctx); err != nil {
		return "", err
	}
	return taskID, nil
}

// ReconcileMonitoring queues versioned snapshots for every product, including inactive products.
func (s *Store) ReconcileMonitoring(ctx context.Context) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollbackTx(ctx, tx)
	rows, err := tx.Query(ctx, `SELECT id,url,platform,monitor_version FROM products ORDER BY id FOR UPDATE`)
	if err != nil {
		return err
	}
	type product struct {
		id, url, platform string
		version           int64
	}
	var all []product
	for rows.Next() {
		var p product
		if err := rows.Scan(&p.id, &p.url, &p.platform, &p.version); err != nil {
			rows.Close()
			return err
		}
		all = append(all, p)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, p := range all {
		p.version++
		if _, err = tx.Exec(ctx, `UPDATE products SET monitor_version=$2 WHERE id=$1`, p.id, p.version); err != nil {
			return err
		}
		if err = s.snapshot(ctx, tx, p.id, p.version, p.url, p.platform); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
