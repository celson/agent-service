FROM golang:1.22-alpine AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o /agent ./cmd/server

# ── Runtime image ─────────────────────────────────────────────────────────────
FROM alpine:3.19

# Node.js para rodar MCP servers via npx
RUN apk add --no-cache ca-certificates python3 nodejs npm

WORKDIR /app
COPY --from=builder /agent /agent

RUN mkdir -p /app/files

EXPOSE 8080
ENTRYPOINT ["/agent"]
