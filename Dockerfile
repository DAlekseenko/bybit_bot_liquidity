FROM golang:1.26.10-alpine AS build

ARG GOPROXY=https://proxy.golang.org,direct
ARG GOSUMDB=sum.golang.org

WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=secret,id=custom_ca,required=false \
    if [ -s /run/secrets/custom_ca ]; then \
        export SSL_CERT_FILE=/run/secrets/custom_ca; \
    fi; \
    go mod download

COPY cmd ./cmd
COPY internal ./internal
COPY pkg ./pkg

RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/bot ./cmd/bot

FROM alpine:3.22

RUN addgroup -S bot \
    && adduser -S -G bot bot \
    && mkdir -p /app/configs /app/data \
    && chown -R bot:bot /app

WORKDIR /app
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build --chown=bot:bot /out/bot /app/bot
COPY --chown=bot:bot configs /app/configs

USER bot

ENTRYPOINT ["/app/bot"]
CMD ["-config", "/app/configs/config.yaml"]
