module github.com/Gergov00/pricescount/services/notifier

go 1.26

require (
	github.com/Gergov00/pricescount/shared v0.0.0-00010101000000-000000000000
	github.com/rabbitmq/amqp091-go v1.9.0
)

replace github.com/Gergov00/pricescount/shared => ../../shared
