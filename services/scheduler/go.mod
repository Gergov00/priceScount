module github.com/Gergov00/pricescount/services/scheduler

go 1.26

require (
	github.com/Gergov00/pricescount/shared v0.0.0-00010101000000-000000000000
	github.com/google/uuid v1.6.0
	github.com/jackc/pgx/v5 v5.6.0
	github.com/rabbitmq/amqp091-go v1.9.0
	go.uber.org/fx v1.24.0
)

require (
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20221227161230-091c0ba34f0a // indirect
	github.com/jackc/puddle/v2 v2.2.1 // indirect
	go.uber.org/dig v1.19.0 // indirect
	go.uber.org/multierr v1.10.0 // indirect
	go.uber.org/zap v1.26.0 // indirect
	golang.org/x/crypto v0.17.0 // indirect
	golang.org/x/sync v0.1.0 // indirect
	golang.org/x/sys v0.42.0 // indirect
	golang.org/x/text v0.14.0 // indirect
)

replace github.com/Gergov00/pricescount/shared => ../../shared
