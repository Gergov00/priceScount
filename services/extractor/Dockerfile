# Build context must be the repository root:
#   docker build -f services/extractor/Dockerfile .
FROM golang:1.26-alpine AS builder
WORKDIR /build

COPY go.work* ./
COPY shared/go.mod              shared/go.mod
COPY services/discovery/go.mod  services/discovery/go.mod
COPY services/scheduler/go.mod  services/scheduler/go.mod
COPY services/extractor/go.mod  services/extractor/go.mod
COPY services/notifier/go.mod   services/notifier/go.mod
RUN go work sync

COPY shared/   shared/
COPY services/extractor/ services/extractor/
WORKDIR /build/services/extractor
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /bin/extractor ./cmd/

FROM alpine:3.19
RUN apk add --no-cache ca-certificates tzdata chromium
ENV CHROMIUM_PATH=/usr/bin/chromium-browser
COPY --from=builder /bin/extractor /bin/extractor
ENTRYPOINT ["/bin/extractor"]
