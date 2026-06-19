FROM golang:1.26-bookworm AS builder

WORKDIR /src
ENV GOPROXY=https://goproxy.cn,direct

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/transit-hub ./cmd/transit-hub

FROM debian:bookworm-slim

WORKDIR /app
RUN mkdir -p /app/data /app/configs /etc/ssl/certs

COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=builder /out/transit-hub /app/transit-hub

EXPOSE 8080

CMD ["/app/transit-hub"]
