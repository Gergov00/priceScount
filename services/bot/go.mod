module github.com/Gergov00/pricescount/services/bot

go 1.26

require (
	github.com/go-telegram-bot-api/telegram-bot-api/v5 v5.5.1
	go.uber.org/fx v1.24.0
)

require (
	go.uber.org/dig v1.19.0 // indirect
	go.uber.org/multierr v1.10.0 // indirect
	go.uber.org/zap v1.26.0 // indirect
	golang.org/x/sys v0.0.0-20220412211240-33da011f77ad // indirect
)

replace github.com/Gergov00/pricescount/shared => ../../shared
