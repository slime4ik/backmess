# ---------- build stage ----------
FROM golang:1.26-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# только сервер: он без cgo, десктоп-клиент собирается отдельно на своих ОС
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o backmess ./cmd/backmess

# ---------- run stage ----------
FROM alpine:latest

RUN apk add --no-cache ca-certificates

WORKDIR /app

COPY --from=builder /app/backmess .

VOLUME /app/data

# 80/443 — при DOMAIN (https сам получит сертификат), 8080 — без домена,
# 8443 udp+tcp — весь WebRTC-трафик
EXPOSE 80 443 8080 8443/udp 8443/tcp

CMD ["./backmess"]
