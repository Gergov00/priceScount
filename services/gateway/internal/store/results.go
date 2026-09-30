package store

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/Gergov00/pricescount/shared/pkg/broker"
	"github.com/Gergov00/pricescount/shared/pkg/contracts"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ErrInvalidResult marks a permanently malformed new-protocol result.
var ErrInvalidResult = errors.New("invalid price result")

// ProcessPriceResult atomically deduplicates results and saves history, state transitions, and notifications.
func (s *Store) ProcessPriceResult(ctx context.Context, result contracts.PriceResult) error {
	if _, err := uuid.Parse(result.TaskID); err != nil {
		return fmt.Errorf("task id: %w: %v", ErrInvalidResult, err)
	}
	if result.LookupID != "" && result.ProductID != "" {
		return fmt.Errorf("result has both lookup and product ids: %w", ErrInvalidResult)
	}
	if result.LookupID != "" {
		if _, err := uuid.Parse(result.LookupID); err != nil {
			return fmt.Errorf("lookup id: %w: %v", ErrInvalidResult, err)
		}
		if result.Success && (math.IsNaN(result.Price) || math.IsInf(result.Price, 0) || result.Price < 0) {
			return fmt.Errorf("price: %w", ErrInvalidResult)
		}
		tx, err := s.db.Begin(ctx)
		if err != nil {
			return err
		}
		defer rollbackTx(ctx, tx)
		tag, err := tx.Exec(ctx, `INSERT INTO processed_price_results(task_id) VALUES($1) ON CONFLICT DO NOTHING`, result.TaskID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return tx.Commit(ctx)
		}
		if result.Success {
			_, err = tx.Exec(ctx, `UPDATE lookup_requests SET status='done',name=$2,price=$3,expires_at=NOW()+INTERVAL '1 hour' WHERE id=$1`, result.LookupID, result.Name, result.Price)
		} else {
			_, err = tx.Exec(ctx, `UPDATE lookup_requests SET status='failed',error=$2 WHERE id=$1`, result.LookupID, result.Error)
		}
		if err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	if _, err := uuid.Parse(result.ProductID); err != nil {
		return fmt.Errorf("product id: %w: %v", ErrInvalidResult, err)
	}
	if result.Success && (math.IsNaN(result.Price) || math.IsInf(result.Price, 0) || result.Price < 0) {
		return fmt.Errorf("price: %w", ErrInvalidResult)
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollbackTx(ctx, tx)
	var url, name string
	var latest *time.Time
	err = tx.QueryRow(ctx, `SELECT url,name,latest_result_at FROM products WHERE id=$1 FOR UPDATE`, result.ProductID).Scan(&url, &name, &latest)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("product: %w", ErrInvalidResult)
	}
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `INSERT INTO processed_price_results(task_id) VALUES($1) ON CONFLICT DO NOTHING`, result.TaskID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return tx.Commit(ctx)
	}
	var forceChat *int64
	var forceProduct string
	var chatID int64
	err = tx.QueryRow(ctx, `SELECT product_id,chat_id FROM force_requests WHERE task_id=$1`, result.TaskID).Scan(&forceProduct, &chatID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if errors.Is(err, pgx.ErrNoRows) {
		if result.Force {
			return tx.Commit(ctx)
		}
	} else if forceProduct != result.ProductID {
		return tx.Commit(ctx)
	} else {
		forceChat = &chatID
	}
	if result.Success && result.ScrapedAt.IsZero() {
		return fmt.Errorf("scraped_at: %w", ErrInvalidResult)
	}
	if result.Success {
		if _, err = tx.Exec(ctx, `INSERT INTO price_history(product_id,price,currency,scraped_at,task_id) VALUES($1,$2,$3,$4,$5)`, result.ProductID, result.Price, result.Currency, result.ScrapedAt, result.TaskID); err != nil {
			return err
		}
	}
	stale := result.Success && latest != nil && result.ScrapedAt.Before(*latest)
	if result.Success && !stale {
		if _, err = tx.Exec(ctx, `UPDATE products SET latest_result_at=$2 WHERE id=$1`, result.ProductID, result.ScrapedAt); err != nil {
			return err
		}
	}
	if forceChat != nil {
		text := fmt.Sprintf("📊 Текущая цена\n\n%s\n%s\n\nЦена: %.0f %s", name, url, result.Price, result.Currency)
		if !result.Success {
			text = fmt.Sprintf("⚠️ Не удалось проверить цену\n\n%s\n%s\n\n%s", name, url, result.Error)
		}
		task := contracts.NotifyTask{TaskID: result.TaskID, Channel: "telegram", Target: strconv.FormatInt(*forceChat, 10), Text: text}
		if err = s.enqueue(ctx, tx, "force-result:"+result.TaskID, broker.QueueNotifyTasks, task); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	if !result.Success {
		return tx.Commit(ctx)
	}
	if stale {
		return tx.Commit(ctx)
	}
	rows, err := tx.Query(ctx, `SELECT s.id,u.chat_id,COALESCE(s.min_price,0),COALESCE(s.max_price,0),s.alert_state,s.alert_version FROM subscriptions s JOIN users u ON u.id=s.user_id WHERE s.product_id=$1 AND s.active AND NOT s.paused ORDER BY s.id FOR UPDATE OF s`, result.ProductID)
	if err != nil {
		return err
	}
	type alertRow struct {
		id       string
		chat     int64
		min, max float64
		state    string
		version  int64
	}
	var subs []alertRow
	for rows.Next() {
		var a alertRow
		if err := rows.Scan(&a.id, &a.chat, &a.min, &a.max, &a.state, &a.version); err != nil {
			rows.Close()
			return err
		}
		subs = append(subs, a)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, sub := range subs {
		next := classify(result.Price, sub.min, sub.max)
		if next == sub.state {
			continue
		}
		sub.version++
		if _, err = tx.Exec(ctx, `UPDATE subscriptions SET alert_state=$2,alert_version=$3 WHERE id=$1`, sub.id, next, sub.version); err != nil {
			return err
		}
		if next == "" {
			continue
		}
		task := contracts.NotifyTask{TaskID: uuid.NewString(), Channel: "telegram", Target: strconv.FormatInt(sub.chat, 10), Direction: next, Text: alertText(name, url, result.Price, result.Currency, next, sub.min, sub.max)}
		if err = s.enqueue(ctx, tx, fmt.Sprintf("alert:%s:%d", sub.id, sub.version), broker.QueueNotifyTasks, task); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
func classify(price, minPrice, maxPrice float64) string {
	if minPrice > 0 && price < minPrice {
		return "down"
	}
	if maxPrice > 0 && price > maxPrice {
		return "up"
	}
	return ""
}
func alertText(name, url string, price float64, currency, direction string, minPrice, maxPrice float64) string {
	if direction == "down" {
		return fmt.Sprintf("📉 Цена упала!\n\n%s\n%s\n\nЦена: %.0f %s\nВаш минимум: %.0f %s", name, url, price, currency, minPrice, currency)
	}
	return fmt.Sprintf("📈 Цена выросла!\n\n%s\n%s\n\nЦена: %.0f %s\nВаш максимум: %.0f %s", name, url, price, currency, maxPrice, currency)
}
