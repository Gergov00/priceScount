package broker

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

func TestAwaitConfirmDrainsQueuedReturn(t *testing.T) {
	returns := make(chan amqp.Return, 1)
	confirms := make(chan amqp.Confirmation, 1)
	returns <- amqp.Return{ReplyText: "NO_ROUTE"}
	confirms <- amqp.Confirmation{Ack: true}
	if err := awaitConfirm(context.Background(), "missing", returns, confirms); err == nil {
		t.Fatal("awaitConfirm() error=nil with both return and ack queued")
	}
}

func TestOpenPublisherCancellationWithStalledPeer(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan struct{})
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			close(accepted)
			defer conn.Close()
			_, _ = io.Copy(io.Discard, conn)
		}
	}()
	c := &Connection{url: fmt.Sprintf("amqp://guest:guest@%s/", listener.Addr()), publishGate: make(chan struct{}, 1)}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, _, err = c.openPublisher(ctx)
	if err == nil {
		t.Fatal("openPublisher() error=nil for stalled peer")
	}
	if time.Since(started) > time.Second {
		t.Fatalf("openPublisher() took %s after context cancellation", time.Since(started))
	}
	select {
	case <-accepted:
	case <-time.After(time.Second):
		t.Fatal("server did not accept publisher TCP connection")
	}
}

func TestRegisterPublisherSocketAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	local, peer := net.Pipe()
	defer peer.Close()
	var mu sync.Mutex
	var socket net.Conn
	var timer *time.Timer
	var timerDone chan struct{}
	if err := registerPublisherSocket(ctx, local, &mu, &socket, &timer, &timerDone); err == nil {
		t.Fatal("registerPublisherSocket() error=nil for canceled context")
	}
	if socket != nil {
		t.Fatal("canceled connection was published as the registered socket")
	}
	if _, err := peer.Write([]byte("write after cancellation")); err == nil {
		t.Fatal("registered socket remained open after cancellation")
	}
}
