package broker

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

const (
	QueueLookupTasks   = "lookup.tasks"
	QueueScraperTasks  = "scraper.tasks"
	QueuePriceResults  = "price.results"
	QueueTrackRequests = "track.requests"
	QueueNotifyTasks   = "notify.tasks"
	QueueNotifyRetry   = "notify.retry"
	QueueNotifyDead    = "notify.dead"
	NotifyRetryDelay   = 30 * time.Second
	dialTimeout        = 5 * time.Second
	closeTimeout       = time.Second
)

// Connection owns independent consumer and publisher channels. Publishing is
// serialized because confirm and return notifications are channel-scoped.
type Connection struct {
	url         string
	mu          sync.Mutex
	conn        *amqp.Connection
	pubConn     *amqp.Connection
	pubNetConn  net.Conn
	ch          *amqp.Channel
	publishGate chan struct{}
	publishCh   *amqp.Channel
	confirms    <-chan amqp.Confirmation
	returns     <-chan amqp.Return
	closed      bool
}

func NewConnection(rawURL string) (*Connection, error) {
	c := &Connection{url: rawURL, publishGate: make(chan struct{}, 1)}
	if err := c.dial(); err != nil {
		return nil, err
	}
	return c, nil
}

func ConnectWithRetry(rawURL string, maxAttempts int) (*Connection, error) {
	var lastErr error
	for i := 1; i <= maxAttempts; i++ {
		c, err := NewConnection(rawURL)
		if err == nil {
			return c, nil
		}
		lastErr = err
		wait := time.Duration(i*2) * time.Second
		slog.Warn("rabbitmq not ready, retrying", "attempt", i, "max", maxAttempts, "wait", wait)
		time.Sleep(wait)
	}
	return nil, fmt.Errorf("rabbitmq: failed after %d attempts: %w", maxAttempts, lastErr)
}

func dialRabbit(url string) (*amqp.Connection, error) {
	config := amqp.Config{Dial: func(network, addr string) (net.Conn, error) { return net.DialTimeout(network, addr, dialTimeout) }}
	return amqp.DialConfig(url, config)
}

func (c *Connection) dial() error {
	conn, err := dialRabbit(c.url)
	if err != nil {
		return fmt.Errorf("amqp dial: %w", err)
	}
	consumer, err := conn.Channel()
	if err != nil {
		conn.Close()
		return fmt.Errorf("open consumer channel: %w", err)
	}
	if err := consumer.Qos(10, 0, false); err != nil {
		consumer.Close()
		conn.Close()
		return fmt.Errorf("qos: %w", err)
	}
	pubConn, publisher, err := c.openPublisher(context.Background())
	if err != nil {
		consumer.Close()
		conn.Close()
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		publisher.Close()
		consumer.Close()
		conn.Close()
		return fmt.Errorf("rabbitmq connection is closed")
	}
	c.conn, c.ch, c.pubConn, c.publishCh = conn, consumer, pubConn, publisher
	return nil
}

func (c *Connection) openPublisher(ctx context.Context) (*amqp.Connection, *amqp.Channel, error) {
	var netConn net.Conn
	var netMu sync.Mutex
	var setupTimer *time.Timer
	var timerDone chan struct{}
	closed := make(chan struct{})
	stopCancel := context.AfterFunc(ctx, func() {
		netMu.Lock()
		defer netMu.Unlock()
		if netConn != nil {
			_ = netConn.Close()
		}
		close(closed)
	})
	setupFinished := false
	finishSetup := func() bool {
		cancelStopped := stopCancel()
		if !cancelStopped {
			<-closed
		}
		timerStopped := true
		if setupTimer != nil {
			timerStopped = setupTimer.Stop()
			if !timerStopped {
				<-timerDone
			}
		}
		setupFinished = true
		return cancelStopped && timerStopped && ctx.Err() == nil
	}
	defer func() {
		if !setupFinished {
			_ = finishSetup()
		}
	}()
	config := amqp.Config{Dial: func(network, addr string) (net.Conn, error) {
		conn, err := (&net.Dialer{Timeout: dialTimeout}).DialContext(ctx, network, addr)
		if err != nil {
			if conn != nil {
				_ = conn.Close()
			}
			return nil, err
		}
		if err := registerPublisherSocket(ctx, conn, &netMu, &netConn, &setupTimer, &timerDone); err != nil {
			return nil, err
		}
		return conn, err
	}}
	conn, err := amqp.DialConfig(c.url, config)
	if err != nil {
		return nil, nil, fmt.Errorf("publisher amqp dial: %w", err)
	}
	if err := setupDeadline(ctx, netConn); err != nil {
		_ = netConn.Close()
		return nil, nil, err
	}
	c.mu.Lock()
	c.pubNetConn = netConn
	c.mu.Unlock()
	ch, err := conn.Channel()
	if err != nil {
		_ = netConn.Close()
		_ = conn.CloseDeadline(time.Now().Add(closeTimeout))
		return nil, nil, fmt.Errorf("open publisher channel: %w", err)
	}
	if err := setupDeadline(ctx, netConn); err != nil {
		_ = netConn.Close()
		_ = conn.CloseDeadline(time.Now().Add(closeTimeout))
		return nil, nil, err
	}
	if err := ch.Confirm(false); err != nil {
		_ = netConn.Close()
		_ = conn.CloseDeadline(time.Now().Add(closeTimeout))
		return nil, nil, fmt.Errorf("enable publisher confirms: %w", err)
	}
	if err := setupDeadline(ctx, netConn); err != nil {
		_ = netConn.Close()
		_ = conn.CloseDeadline(time.Now().Add(closeTimeout))
		return nil, nil, err
	}
	if !finishSetup() {
		_ = netConn.Close()
		_ = conn.CloseDeadline(time.Now().Add(closeTimeout))
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		return nil, nil, fmt.Errorf("publisher AMQP setup timed out")
	}
	_ = netConn.SetDeadline(time.Time{})
	c.mu.Lock()
	c.confirms = ch.NotifyPublish(make(chan amqp.Confirmation, 1))
	c.returns = ch.NotifyReturn(make(chan amqp.Return, 1))
	c.mu.Unlock()
	return conn, ch, nil
}

func registerPublisherSocket(ctx context.Context, conn net.Conn, mu *sync.Mutex, socket *net.Conn, timer **time.Timer, timerDone *chan struct{}) error {
	mu.Lock()
	defer mu.Unlock()
	*socket = conn
	if err := ctx.Err(); err != nil {
		*socket = nil
		_ = conn.Close()
		return err
	}
	if err := conn.SetDeadline(time.Now().Add(dialTimeout)); err != nil {
		*socket = nil
		_ = conn.Close()
		return err
	}
	*timerDone = make(chan struct{})
	*timer = time.AfterFunc(dialTimeout, func() { defer close(*timerDone); _ = conn.Close() })
	return nil
}

func (c *Connection) DeclareQueue(name string) error {
	c.mu.Lock()
	ch := c.ch
	c.mu.Unlock()
	if ch == nil {
		return fmt.Errorf("rabbitmq consumer channel unavailable")
	}
	_, err := ch.QueueDeclare(name, true, false, false, false, nil)
	return err
}

// DeclareNotifyQueues creates the durable notification task, retry and dead-letter
// queues. The retry queue expires messages back to the supplied task queue.
func (c *Connection) DeclareNotifyQueues(taskQueue, retryQueue, deadQueue string, retryDelay time.Duration) error {
	c.mu.Lock()
	conn := c.conn
	closed := c.closed
	c.mu.Unlock()
	if closed || conn == nil {
		return fmt.Errorf("connection unavailable")
	}
	ch, err := conn.Channel()
	if err != nil {
		return fmt.Errorf("open notifier declaration channel: %w", err)
	}
	defer func() {
		if err := ch.Close(); err != nil {
			slog.Warn("close notifier declaration channel", "error", err)
		}
	}()
	if _, err := ch.QueueDeclare(taskQueue, true, false, false, false, nil); err != nil {
		return fmt.Errorf("declare %s: %w", taskQueue, err)
	}
	if _, err := ch.QueueDeclare(retryQueue, true, false, false, false, amqp.Table{
		"x-message-ttl":             int32(retryDelay / time.Millisecond),
		"x-dead-letter-exchange":    "",
		"x-dead-letter-routing-key": taskQueue,
	}); err != nil {
		return fmt.Errorf("declare %s: %w", retryQueue, err)
	}
	if _, err := ch.QueueDeclare(deadQueue, true, false, false, false, nil); err != nil {
		return fmt.Errorf("declare %s: %w", deadQueue, err)
	}
	return nil
}

func (c *Connection) Publish(ctx context.Context, queue string, v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("json marshal: %w", err)
	}
	msg := amqp.Publishing{ContentType: "application/json", DeliveryMode: amqp.Persistent, Body: body}
	err = c.publishOn(ctx, queue, msg)
	if err == nil {
		return nil
	}
	if ctx.Err() != nil || !c.isDead() {
		return err
	}
	if err := c.redial(ctx); err != nil {
		return fmt.Errorf("publish redial: %w", err)
	}
	return c.publishOn(ctx, queue, msg)
}

func (c *Connection) publishOn(ctx context.Context, queue string, msg amqp.Publishing) error {
	// The mutex protects only the publisher channel and its notification streams;
	// consumer channel reads and Close never wait for an outstanding confirm.
	select {
	case c.publishGate <- struct{}{}:
		defer func() { <-c.publishGate }()
	case <-ctx.Done():
		return ctx.Err()
	}
	c.mu.Lock()
	ch := c.publishCh
	closed := c.closed
	c.mu.Unlock()
	if closed || ch == nil || ch.IsClosed() {
		return fmt.Errorf("publisher channel unavailable")
	}
	c.mu.Lock()
	confirms, returns := c.confirms, c.returns
	c.mu.Unlock()
	stopDeadline, err := c.setPublishDeadline(ctx)
	if err != nil {
		c.retirePublisher(ch)
		return fmt.Errorf("set publish deadline: %w", err)
	}
	if err := ch.PublishWithContext(ctx, "", queue, true, false, msg); err != nil {
		stopDeadline()
		c.retirePublisher(ch)
		return fmt.Errorf("publish: %w", err)
	}
	stopDeadline()
	_ = setWriteDeadline(c.publisherNetConn(), time.Time{})
	err = awaitConfirm(ctx, queue, returns, confirms)
	if ctx.Err() != nil {
		c.retirePublisher(ch)
	}
	return err
}

func awaitConfirm(ctx context.Context, queue string, returns <-chan amqp.Return, confirms <-chan amqp.Confirmation) error {
	var returned *amqp.Return
	for {
		select {
		case ret, ok := <-returns:
			if ok {
				returned = &ret
				returns = nil
				continue
			}
			returns = nil
		case confirm, ok := <-confirms:
			if !ok {
				return fmt.Errorf("publisher confirm channel closed")
			}
			if returned == nil {
				select {
				case ret, ok := <-returns:
					if ok {
						returned = &ret
					}
				default:
				}
			}
			if returned != nil {
				return fmt.Errorf("message unroutable to %q: %s", queue, returned.ReplyText)
			}
			if !confirm.Ack {
				return fmt.Errorf("publish to %q negatively acknowledged", queue)
			}
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func setWriteDeadline(conn net.Conn, deadline time.Time) error {
	if conn == nil {
		return nil
	}
	return conn.SetWriteDeadline(deadline)
}

func setupDeadline(ctx context.Context, conn net.Conn) error {
	if conn == nil {
		return fmt.Errorf("publisher transport unavailable")
	}
	deadline := time.Now().Add(dialTimeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func (c *Connection) publisherNetConn() net.Conn {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pubNetConn
}

func (c *Connection) setPublishDeadline(ctx context.Context) (func() bool, error) {
	conn := c.publisherNetConn()
	if conn == nil {
		return nil, fmt.Errorf("publisher transport unavailable")
	}
	deadline := time.Now().Add(dialTimeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	if err := conn.SetWriteDeadline(deadline); err != nil {
		return nil, err
	}
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { defer close(done); _ = conn.SetWriteDeadline(time.Now()) })
	return func() bool {
		stopped := stop()
		if !stopped {
			<-done
		}
		return stopped
	}, nil
}

func (c *Connection) retirePublisher(ch *amqp.Channel) {
	c.mu.Lock()
	var conn *amqp.Connection
	var socket net.Conn
	if c.publishCh == ch {
		c.publishCh, c.confirms, c.returns = nil, nil, nil
		conn, c.pubConn = c.pubConn, nil
		socket, c.pubNetConn = c.pubNetConn, nil
	}
	c.mu.Unlock()
	if socket != nil {
		_ = socket.Close()
	}
	if conn != nil {
		_ = conn.CloseDeadline(time.Now().Add(closeTimeout))
	}
}

func (c *Connection) isDead() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed || c.pubConn == nil || c.pubConn.IsClosed() || c.publishCh == nil || c.publishCh.IsClosed()
}

func (c *Connection) redial(ctx context.Context) error {
	select {
	case c.publishGate <- struct{}{}:
		defer func() { <-c.publishGate }()
	case <-ctx.Done():
		return ctx.Err()
	}
	slog.Warn("rabbitmq publisher connection lost, redialing")
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return fmt.Errorf("rabbitmq connection is closed")
	}
	if c.pubConn != nil && !c.pubConn.IsClosed() && c.publishCh != nil && !c.publishCh.IsClosed() {
		c.mu.Unlock()
		return nil
	}
	old, oldSocket := c.pubConn, c.pubNetConn
	c.pubConn, c.publishCh, c.confirms, c.returns, c.pubNetConn = nil, nil, nil, nil, nil
	c.mu.Unlock()
	if oldSocket != nil {
		_ = oldSocket.Close()
	}
	if old != nil {
		_ = old.CloseDeadline(time.Now().Add(closeTimeout))
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	pubConn, publisher, err := c.openPublisher(ctx)
	if err != nil {
		return err
	}
	c.mu.Lock()
	if c.closed {
		socket := c.pubNetConn
		c.mu.Unlock()
		if socket != nil {
			_ = socket.Close()
		}
		_ = pubConn.CloseDeadline(time.Now().Add(closeTimeout))
		return fmt.Errorf("rabbitmq connection is closed")
	}
	c.pubConn, c.publishCh = pubConn, publisher
	c.mu.Unlock()
	return nil
}

func (c *Connection) Consume(queue, consumer string) (<-chan amqp.Delivery, error) {
	c.mu.Lock()
	ch := c.ch
	c.mu.Unlock()
	if ch == nil {
		return nil, fmt.Errorf("consumer channel unavailable")
	}
	return ch.Consume(queue, consumer, false, false, false, false, nil)
}

func (c *Connection) ConsumeWithPrefetch(queue, consumer string, prefetch int) (<-chan amqp.Delivery, error) {
	c.mu.Lock()
	conn := c.conn
	closed := c.closed
	c.mu.Unlock()
	if closed || conn == nil {
		return nil, fmt.Errorf("connection unavailable")
	}
	ch, err := conn.Channel()
	if err != nil {
		return nil, fmt.Errorf("open channel for %s: %w", queue, err)
	}
	if err := ch.Qos(prefetch, 0, false); err != nil {
		ch.Close()
		return nil, fmt.Errorf("qos for %s: %w", queue, err)
	}
	if _, err := ch.QueueDeclare(queue, true, false, false, false, nil); err != nil {
		ch.Close()
		return nil, fmt.Errorf("declare queue %s: %w", queue, err)
	}
	deliveries, err := ch.Consume(queue, consumer, false, false, false, false, nil)
	if err != nil {
		ch.Close()
		return nil, fmt.Errorf("consume %s: %w", queue, err)
	}
	return deliveries, nil
}

func (c *Connection) Close() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	conn, pubConn, socket := c.conn, c.pubConn, c.pubNetConn
	c.pubConn, c.publishCh, c.confirms, c.returns, c.pubNetConn = nil, nil, nil, nil, nil
	c.mu.Unlock()
	// Force-close transports before bounded AMQP close handshakes so no close
	// waits behind a peer that stopped reading or responding.
	if socket != nil {
		_ = socket.Close()
	}
	if pubConn != nil {
		_ = pubConn.CloseDeadline(time.Now().Add(closeTimeout))
	}
	if conn != nil {
		_ = conn.CloseDeadline(time.Now().Add(closeTimeout))
	}
}
