FROM golang:1.24-alpine AS build

WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
COPY pkg ./pkg

RUN go build -o /out/bot ./cmd/bot

FROM alpine:3.22

WORKDIR /app
COPY --from=build /out/bot /app/bot
COPY configs /app/configs

ENTRYPOINT ["/app/bot"]
CMD ["-config", "/app/configs/config.yaml"]
