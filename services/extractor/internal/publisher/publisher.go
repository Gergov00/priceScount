package publisher

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/Gergov00/pricescount/shared/pkg/broker"
	"github.com/Gergov00/pricescount/shared/pkg/contracts"
)

type Publisher struct {
	conn *broker.Connection
}

func New(conn *broker.Connection) *Publisher {
	return &Publisher{conn: conn}
}

func (p *Publisher) PublishResult(ctx context.Context, result contracts.PriceResult) error {
	if err := p.conn.Publish(ctx, broker.QueuePriceResults, result); err != nil {
		return fmt.Errorf("publish price result: %w", err)
	}
	slog.Info("published price result",
		"url", result.URL,
		"price", result.Price,
		"currency", result.Currency,
		"success", result.Success,
	)
	return nil
}
