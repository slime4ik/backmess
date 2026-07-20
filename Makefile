# backmess
APP_TAGS = -tags nolibopusfile

.PHONY: server app run-server run-app smoke

server: ## сервер (без cgo, встроенный веб-фолбэк внутри)
	CGO_ENABLED=0 go build -ldflags="-s -w" -o backmess ./cmd/backmess

app: ## десктоп-клиент (нужны: libopus, pkg-config; см. README)
	go build $(APP_TAGS) -ldflags="-s -w" -o backmess-app ./cmd/backmess-app

run-server:
	go run ./cmd/backmess

run-app:
	go run $(APP_TAGS) ./cmd/backmess-app
