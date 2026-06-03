module github.com/Gergov00/pricescount/services/notifier

go 1.26

require (
	github.com/Gergov00/pricescount/shared v0.0.0-00010101000000-000000000000
	github.com/rabbitmq/amqp091-go v1.9.0
	go.uber.org/fx v1.24.0
)

require (
	go.uber.org/dig v1.19.0 // indirect
	go.uber.org/multierr v1.10.0 // indirect
	go.uber.org/zap v1.26.0 // indirect
	golang.org/x/sys v0.42.0 // indirect
)

replace github.com/Gergov00/pricescount/shared => ../../shared
