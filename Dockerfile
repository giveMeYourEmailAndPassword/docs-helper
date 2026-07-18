FROM golang:1.26-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -o /docs-helper ./cmd/helper

FROM alpine:3.21

RUN apk add --no-cache ca-certificates

COPY --from=builder /docs-helper /usr/local/bin/docs-helper
COPY migrations /migrations

EXPOSE 8080

ENTRYPOINT ["docs-helper"]
