//go:build integration

package broker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

func TestPublishConsumeAckAndRequeue(t *testing.T) {
	t.Parallel()
	url := rabbitMQURL(t)
	queue := uniqueIntegrationQueue("ack-requeue")
	connection := newIntegrationConnection(t, url)
	declareIntegrationQueue(t, connection, queue)

	want := map[string]string{"task_id": "integration-task", "kind": "price lookup"}
	if err := connection.Publish(context.Background(), queue, want); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	deliveries, err := connection.ConsumeWithPrefetch(queue, "codex-integration", 1)
	if err != nil {
		t.Fatalf("ConsumeWithPrefetch() error = %v", err)
	}

	first := receiveDelivery(t, deliveries)
	assertPersistentJSON(t, first)
	assertJSONBody(t, first.Body, want)
	if first.Redelivered {
		t.Fatal("first delivery has Redelivered=true")
	}
	if err := first.Nack(false, true); err != nil {
		t.Fatalf("Nack(requeue=true) error = %v", err)
	}

	second := receiveDelivery(t, deliveries)
	assertPersistentJSON(t, second)
	assertJSONBody(t, second.Body, want)
	if !second.Redelivered {
		t.Fatal("requeued delivery has Redelivered=false")
	}
	if err := second.Ack(false); err != nil {
		t.Fatalf("Ack() error = %v", err)
	}
}

func TestPublishMarshalErrorDoesNotEnqueue(t *testing.T) {
	t.Parallel()
	url := rabbitMQURL(t)
	queue := uniqueIntegrationQueue("marshal-error")
	connection := newIntegrationConnection(t, url)
	declareIntegrationQueue(t, connection, queue)

	if err := connection.Publish(context.Background(), queue, struct {
		Unsupported chan int `json:"unsupported"`
	}{Unsupported: make(chan int)}); err == nil {
		t.Fatal("Publish() error = nil, want JSON marshal error")
	}

	adminChannel := newAdminChannel(t, url)
	message, ok, err := adminChannel.Get(queue, false)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if ok {
		if err := message.Ack(false); err != nil {
			t.Errorf("Ack() for unexpected message: %v", err)
		}
		t.Fatalf("queue received a message after marshal failure: %q", message.Body)
	}
}

func TestPublishUnroutableFails(t *testing.T) {
	t.Parallel()
	connection := newIntegrationConnection(t, rabbitMQURL(t))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := connection.Publish(ctx, uniqueIntegrationQueue("missing"), map[string]string{"id": "unroutable"}); err == nil {
		t.Fatal("Publish() error = nil for an unroutable mandatory message")
	}
}

func TestPublishCancelled(t *testing.T) {
	t.Parallel()
	connection := newIntegrationConnection(t, rabbitMQURL(t))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := connection.Publish(ctx, uniqueIntegrationQueue("cancelled"), map[string]string{"id": "cancelled"}); err == nil {
		t.Fatal("Publish() error = nil for cancelled context")
	}
}

func TestPublishReconnectsAfterPublisherChannelCloses(t *testing.T) {
	t.Parallel()
	url := rabbitMQURL(t)
	queue := uniqueIntegrationQueue("reconnect")
	connection := newIntegrationConnection(t, url)
	declareIntegrationQueue(t, connection, queue)

	connection.mu.Lock()
	publisher := connection.publishCh
	connection.mu.Unlock()
	if err := publisher.Close(); err != nil {
		t.Fatalf("close publisher channel: %v", err)
	}
	want := map[string]string{"task_id": "after-reconnect"}
	if err := connection.Publish(context.Background(), queue, want); err != nil {
		t.Fatalf("Publish() after channel close error = %v", err)
	}

	adminChannel := newAdminChannel(t, url)
	message, ok, err := adminChannel.Get(queue, false)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if !ok {
		t.Fatal("queue has no message after publisher redial")
	}
	assertPersistentJSON(t, message)
	assertJSONBody(t, message.Body, want)
	if err := message.Ack(false); err != nil {
		t.Fatalf("Ack() error = %v", err)
	}
}

func TestNotifyRetryTTLAndDeadQueueDeclarations(t *testing.T) {
	t.Parallel()
	url := rabbitMQURL(t)
	tasks := uniqueIntegrationQueue("notify-tasks")
	retry := uniqueIntegrationQueue("notify-retry")
	dead := uniqueIntegrationQueue("notify-dead")
	connection := newIntegrationConnection(t, url)
	if err := connection.DeclareNotifyQueues(tasks, retry, dead, 150*time.Millisecond); err != nil {
		t.Fatalf("DeclareNotifyQueues() error = %v", err)
	}
	cleanup := func(queue string) {
		ch := newAdminChannel(t, url)
		if _, err := ch.QueueDelete(queue, false, false, false); err != nil {
			t.Errorf("delete %s: %v", queue, err)
		}
	}
	t.Cleanup(func() { cleanup(tasks); cleanup(retry); cleanup(dead) })

	if err := connection.Publish(context.Background(), retry, map[string]string{"task_id": "retry"}); err != nil {
		t.Fatalf("publish retry: %v", err)
	}
	deliveries, err := connection.Consume(tasks, "notify-ttl-test")
	if err != nil {
		t.Fatalf("consume tasks: %v", err)
	}
	select {
	case d := <-deliveries:
		if err := d.Ack(false); err != nil {
			t.Fatalf("ack dead-lettered retry: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("retry message did not return to task queue after TTL")
	}

	if err := connection.Publish(context.Background(), dead, map[string]string{"task_id": "dead"}); err != nil {
		t.Fatalf("publish dead: %v", err)
	}
	deadDeliveries, err := connection.Consume(dead, "notify-dead-test")
	if err != nil {
		t.Fatalf("consume dead: %v", err)
	}
	if got := receiveDelivery(t, deadDeliveries).Body; string(got) != `{"task_id":"dead"}` {
		t.Fatalf("dead queue body=%s", got)
	}
}

func rabbitMQURL(t *testing.T) string {
	t.Helper()
	url := os.Getenv("TEST_RABBITMQ_URL")
	if url == "" {
		t.Fatal("TEST_RABBITMQ_URL is required for integration tests")
	}
	return url
}

func uniqueIntegrationQueue(label string) string {
	return fmt.Sprintf("codex.integration.%d.%d.%s", os.Getpid(), time.Now().UnixNano(), label)
}

func newIntegrationConnection(t *testing.T, url string) *Connection {
	t.Helper()
	connection, err := NewConnection(url)
	if err != nil {
		t.Fatalf("NewConnection() error = %v", err)
	}
	t.Cleanup(func() { connection.Close() })
	return connection
}

func declareIntegrationQueue(t *testing.T, connection *Connection, queue string) {
	t.Helper()
	if err := connection.DeclareQueue(queue); err != nil {
		t.Fatalf("DeclareQueue(%q) error = %v", queue, err)
	}
	t.Cleanup(func() {
		conn, err := amqp.Dial(os.Getenv("TEST_RABBITMQ_URL"))
		if err != nil {
			t.Errorf("dial RabbitMQ for queue cleanup: %v", err)
			return
		}
		defer func() {
			if err := conn.Close(); err != nil {
				t.Errorf("close RabbitMQ cleanup connection: %v", err)
			}
		}()
		channel, err := conn.Channel()
		if err != nil {
			t.Errorf("open RabbitMQ cleanup channel: %v", err)
			return
		}
		defer func() {
			if err := channel.Close(); err != nil {
				t.Errorf("close RabbitMQ cleanup channel: %v", err)
			}
		}()
		if _, err := channel.QueueDelete(queue, false, false, false); err != nil {
			t.Errorf("delete test queue %q: %v", queue, err)
		}
	})
}

func newAdminChannel(t *testing.T, url string) *amqp.Channel {
	t.Helper()
	conn, err := amqp.Dial(url)
	if err != nil {
		t.Fatalf("dial RabbitMQ admin connection: %v", err)
	}
	t.Cleanup(func() {
		if err := conn.Close(); err != nil {
			t.Errorf("close RabbitMQ admin connection: %v", err)
		}
	})
	channel, err := conn.Channel()
	if err != nil {
		t.Fatalf("open RabbitMQ admin channel: %v", err)
	}
	t.Cleanup(func() {
		if err := channel.Close(); err != nil {
			t.Errorf("close RabbitMQ admin channel: %v", err)
		}
	})
	return channel
}

func receiveDelivery(t *testing.T, deliveries <-chan amqp.Delivery) amqp.Delivery {
	t.Helper()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	select {
	case delivery, ok := <-deliveries:
		if !ok {
			t.Fatal("delivery channel closed before a message arrived")
		}
		return delivery
	case <-timer.C:
		t.Fatal("timed out waiting for RabbitMQ delivery")
		return amqp.Delivery{}
	}
}

func assertPersistentJSON(t *testing.T, delivery amqp.Delivery) {
	t.Helper()
	if delivery.ContentType != "application/json" {
		t.Errorf("ContentType = %q, want application/json", delivery.ContentType)
	}
	if delivery.DeliveryMode != amqp.Persistent {
		t.Errorf("DeliveryMode = %d, want persistent (%d)", delivery.DeliveryMode, amqp.Persistent)
	}
}

func assertJSONBody(t *testing.T, body []byte, want any) {
	t.Helper()
	var got any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("json.Unmarshal(%q) error = %v", body, err)
	}
	wantBytes, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("json.Marshal(want) error = %v", err)
	}
	var wantDecoded any
	if err := json.Unmarshal(wantBytes, &wantDecoded); err != nil {
		t.Fatalf("json.Unmarshal(want) error = %v", err)
	}
	if !reflect.DeepEqual(got, wantDecoded) {
		t.Errorf("message JSON = %s, want %s", body, wantBytes)
	}
}
