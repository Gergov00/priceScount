package publisher

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/Gergov00/pricescount/shared/pkg/broker"
	"github.com/Gergov00/pricescount/shared/pkg/contracts"
)

// Publisher sends discovered URLs to the discovery.urls RabbitMQ queue.
type Publisher struct {
	conn *broker.Connection
}

func New(conn *broker.Connection) *Publisher {
	return &Publisher{conn: conn}
}

// PublishDiscoveredURLs sends each message to the queue, returning on the first error.
func (p *Publisher) PublishDiscoveredURLs(ctx context.Context, msgs []contracts.DiscoveredURL) error {
	for _, msg := range msgs {
		if err := p.conn.Publish(ctx, broker.QueueDiscoveryURLs, msg); err != nil {
			return fmt.Errorf("publish %s: %w", msg.URL, err)
		}
		slog.Info("published discovered URL",
			"url", msg.URL,
			"source", msg.Source,
			"product_id", msg.ProductID,
		)
	}
	return nil
}
