# Build stage — Go version must match go.mod (currently 1.25.7)
FROM golang:1.25-alpine AS builder
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o kyvik ./cmd/kyvik
RUN CGO_ENABLED=0 go build -o kyvik-sandbox ./cmd/kyvik-sandbox

# Runtime stage
FROM alpine:3.19
RUN apk add --no-cache ca-certificates
# Alpine-specific BusyBox syntax — update if base image changes from Alpine
RUN addgroup -g 1000 kyvik && adduser -D -u 1000 -G kyvik kyvik
WORKDIR /app
COPY --from=builder --chown=kyvik:kyvik /build/kyvik .
COPY --from=builder --chown=kyvik:kyvik /build/kyvik-sandbox .
COPY --from=builder --chown=kyvik:kyvik /build/migrations ./migrations
COPY --from=builder --chown=kyvik:kyvik /build/configs ./configs
COPY --from=builder --chown=kyvik:kyvik /build/web/templates ./web/templates
COPY --from=builder --chown=kyvik:kyvik /build/web/static ./web/static

RUN mkdir -p /app/data && chown kyvik:kyvik /app/data
VOLUME ["/app/data"]
EXPOSE 8080

USER kyvik
HEALTHCHECK --interval=30s --timeout=5s CMD wget -qO- http://localhost:8080/healthz || exit 1
ENTRYPOINT ["./kyvik"]
