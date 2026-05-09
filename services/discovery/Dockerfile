# Build context must be the repository root:
#   docker build -f services/discovery/Dockerfile .
FROM golang:1.26-alpine AS builder
WORKDIR /build

# Copy workspace manifest and all module descriptors so the dependency
# download layer is cached independently of source changes.
COPY go.work* ./
COPY shared/go.mod shared/go.mod
COPY services/discovery/go.mod  services/discovery/go.mod
COPY services/scheduler/go.mod  services/scheduler/go.mod
COPY services/extractor/go.mod  services/extractor/go.mod
COPY services/notifier/go.mod   services/notifier/go.mod
RUN go work sync

# Copy source and build.
COPY shared/ shared/
COPY services/discovery/ services/discovery/
WORKDIR /build/services/discovery
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /bin/discovery ./cmd/

FROM alpine:3.19
RUN apk add --no-cache ca-certificates tzdata
COPY --from=builder /bin/discovery /bin/discovery
EXPOSE 8081
ENTRYPOINT ["/bin/discovery"]
