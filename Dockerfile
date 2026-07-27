FROM golang:1.26-bookworm AS builder

WORKDIR /src
ENV GOPROXY=https://goproxy.cn,direct

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/transit-hub ./cmd/transit-hub
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/transit-hub-migrate ./cmd/transit-hub-migrate
RUN mkdir -p /out/root/app/data /out/root/app/configs /out/root/etc/ssl/certs /out/root/usr/share \
	&& cp /etc/ssl/certs/ca-certificates.crt /out/root/etc/ssl/certs/ca-certificates.crt \
	&& cp -a /usr/share/zoneinfo /out/root/usr/share/zoneinfo

FROM scratch

WORKDIR /app

COPY --from=builder /out/root/ /
COPY --from=builder /out/transit-hub /app/transit-hub
COPY --from=builder /out/transit-hub-migrate /app/transit-hub-migrate

EXPOSE 8080

CMD ["/app/transit-hub"]
