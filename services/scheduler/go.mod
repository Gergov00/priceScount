module github.com/Gergov00/pricescount/services/scheduler

go 1.22.0

require (
	github.com/Gergov00/pricescount/shared v0.0.0-00010101000000-000000000000
	github.com/google/uuid v1.6.0
	github.com/rabbitmq/amqp091-go v1.9.0
	github.com/redis/go-redis/v9 v9.5.1
)

require (
	github.com/cespare/xxhash/v2 v2.2.0 // indirect
	github.com/dgryski/go-rendezvous v0.0.0-20200823014737-9f7001d12a5f // indirect
)



replace github.com/Gergov00/pricescount/shared => ../../shared
