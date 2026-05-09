module github.com/Gergov00/pricescount/services/extractor

go 1.26

require (
	github.com/Gergov00/pricescount/shared v0.0.0-00010101000000-000000000000
	github.com/chromedp/chromedp v0.11.2
	github.com/rabbitmq/amqp091-go v1.9.0
	github.com/redis/go-redis/v9 v9.5.1
)

require (
	github.com/cespare/xxhash/v2 v2.2.0 // indirect
	github.com/chromedp/cdproto v0.0.0-20241022234722-4d5d5faf59fb // indirect
	github.com/chromedp/sysutil v1.1.0 // indirect
	github.com/dgryski/go-rendezvous v0.0.0-20200823014737-9f7001d12a5f // indirect
	github.com/gobwas/httphead v0.1.0 // indirect
	github.com/gobwas/pool v0.2.1 // indirect
	github.com/gobwas/ws v1.4.0 // indirect
	github.com/josharian/intern v1.0.0 // indirect
	github.com/mailru/easyjson v0.7.7 // indirect
	golang.org/x/sys v0.26.0 // indirect
)

replace github.com/Gergov00/pricescount/shared => ../../shared
