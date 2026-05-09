module github.com/Gergov00/pricescount/services/discovery

go 1.22.0

require (
	github.com/Gergov00/pricescount/shared v0.0.0-00010101000000-000000000000
	github.com/google/uuid v1.6.0
)

require github.com/rabbitmq/amqp091-go v1.9.0 // indirect



replace github.com/Gergov00/pricescount/shared => ../../shared
