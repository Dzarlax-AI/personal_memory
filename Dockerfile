FROM golang:1.24-alpine@sha256:8bee1901f1e530bfb4a7850aa7a479d17ae3a18beb6e09064ed54cfd245b7191 AS builder
RUN apk add --no-cache curl

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Bake checksum-locked browser bundles into the embed tree before `go build`
# so //go:embed picks them up. Design-system files come from immutable release
# asset URLs; all downloads are verified before compilation.
ARG DS_VERSION=v1.4.0
ARG VIS_NETWORK_VERSION=9.1.9
ARG VIS_TIMELINE_VERSION=7.7.3
RUN mkdir -p internal/viz/static/assets/vendor && \
    ds_base="https://github.com/dzarlax/design-system/releases/download/${DS_VERSION}" && \
    curl -fsSL "${ds_base}/dzarlax.css" \
        -o internal/viz/static/assets/vendor/dzarlax.css && \
    curl -fsSL "${ds_base}/dzarlax.js" \
        -o internal/viz/static/assets/vendor/dzarlax.js && \
    curl -fsSL "https://unpkg.com/vis-network@${VIS_NETWORK_VERSION}/standalone/umd/vis-network.min.js" \
        -o internal/viz/static/assets/vendor/vis-network.min.js && \
    curl -fsSL "https://unpkg.com/vis-timeline@${VIS_TIMELINE_VERSION}/standalone/umd/vis-timeline-graph2d.min.js" \
        -o internal/viz/static/assets/vendor/vis-timeline-graph2d.min.js && \
    curl -fsSL "https://unpkg.com/vis-timeline@${VIS_TIMELINE_VERSION}/styles/vis-timeline-graph2d.min.css" \
        -o internal/viz/static/assets/vendor/vis-timeline-graph2d.min.css && \
    sha256sum -c build/browser-assets.sha256

RUN CGO_ENABLED=0 go build -o /personal-memory ./cmd/server
RUN CGO_ENABLED=0 go build -o /personal-memory-indexer ./cmd/indexer
RUN CGO_ENABLED=0 go build -o /personal-memory-migrate-ids ./cmd/migrate-memory-ids
RUN CGO_ENABLED=0 go build -o /personal-memory-migrate-lifecycle ./cmd/migrate-memory-lifecycle

FROM alpine:3.21@sha256:48b0309ca019d89d40f670aa1bc06e426dc0931948452e8491e3d65087abc07d
RUN apk add --no-cache ca-certificates
COPY --from=builder /personal-memory /personal-memory
COPY --from=builder /personal-memory-indexer /personal-memory-indexer
COPY --from=builder /personal-memory-migrate-ids /personal-memory-migrate-ids
COPY --from=builder /personal-memory-migrate-lifecycle /personal-memory-migrate-lifecycle

ENTRYPOINT ["/personal-memory"]
