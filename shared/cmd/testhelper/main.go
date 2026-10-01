// Command testhelper provides explicitly test-only tools for isolated integration checks.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/Gergov00/pricescount/shared/pkg/broker"
	amqp "github.com/rabbitmq/amqp091-go"
)

func main() {
	if len(os.Args) < 2 {
		fail("usage: testhelper broker-publish|broker-consume [flags]")
	}
	switch os.Args[1] {
	case "broker-publish":
		brokerPublish(os.Args[2:])
	case "broker-consume":
		brokerConsume(os.Args[2:])
	default:
		fail("unknown helper command")
	}
}

func brokerPublish(args []string) {
	flags := flag.NewFlagSet("broker-publish", flag.ExitOnError)
	url := flags.String("url", "", "test broker URL")
	queue := flags.String("queue", "", "durable test queue")
	body := flags.String("body", "", "persistent test message body")
	if err := flags.Parse(args); err != nil {
		fail("parse broker-publish flags: " + err.Error())
	}
	if *url == "" || *queue == "" || *body == "" {
		fail("url, queue, and body are required")
	}
	conn, err := broker.NewConnection(*url)
	if err != nil {
		fail("dial test broker: " + err.Error())
	}
	defer conn.Close() // Close has no error return in the production broker wrapper.
	if err := conn.DeclareQueue(*queue); err != nil {
		fail("declare durable test queue: " + err.Error())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := conn.Publish(ctx, *queue, json.RawMessage(*body)); err != nil {
		fail("publish persistent test message: " + err.Error())
	}
	if _, err := fmt.Fprintln(os.Stdout, "persistent message publish confirmed"); err != nil {
		fail("write publish status: " + err.Error())
	}
}

func brokerConsume(args []string) {
	flags := flag.NewFlagSet("broker-consume", flag.ExitOnError)
	url := flags.String("url", "", "test broker URL")
	queue := flags.String("queue", "", "durable test queue")
	if err := flags.Parse(args); err != nil {
		fail("parse broker-consume flags: " + err.Error())
	}
	if *url == "" || *queue == "" {
		fail("url and queue are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	config := amqp.Config{Dial: func(network, address string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, address)
	}}
	conn, err := amqp.DialConfig(*url, config)
	if err != nil {
		fail("dial test broker: " + err.Error())
	}
	defer func() {
		if err := conn.Close(); err != nil {
			fmt.Fprintln(os.Stderr, "close test broker connection:", err)
		}
	}()
	ch, err := conn.Channel()
	if err != nil {
		fail("open test channel: " + err.Error())
	}
	defer func() {
		if err := ch.Close(); err != nil {
			fmt.Fprintln(os.Stderr, "close test broker channel:", err)
		}
	}()
	deliveries, err := ch.Consume(*queue, "recovery-test", false, false, false, false, nil)
	if err != nil {
		fail("consume recovered test message: " + err.Error())
	}
	select {
	case delivery, ok := <-deliveries:
		if !ok {
			fail("test queue closed before delivery")
		}
		if err := delivery.Ack(false); err != nil {
			fail("ack recovered test message: " + err.Error())
		}
		if _, err := os.Stdout.Write(delivery.Body); err != nil {
			fail("write recovered message: " + err.Error())
		}
	case <-time.After(10 * time.Second):
		fail("timed out waiting for recovered test message")
	}
}

func fail(message string) {
	fmt.Fprintln(os.Stderr, message)
	os.Exit(1)
}
