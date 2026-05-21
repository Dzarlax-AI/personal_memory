FROM golang:1.24-alpine AS builder
RUN apk add --no-cache curl

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Bake browser bundles into the embed tree before `go build` so
# //go:embed picks them up. DS_REF=latest resolves the latest GitHub release;
# pass a tag/branch/sha to pin it.
# To force a cache-miss on this layer pass --build-arg DS_CACHEBUST=$(date +%s).
ARG DS_REPO=dzarlax/design-system
ARG DS_REF=latest
ARG VIS_NETWORK_VERSION=9.1.9
ARG VIS_TIMELINE_VERSION=7.7.3
ARG DS_CACHEBUST=
RUN mkdir -p internal/viz/static/assets/vendor && \
    echo "cachebust: ${DS_CACHEBUST}" > /dev/null && \
    resolved_ref="${DS_REF}" && \
    if [ "${DS_REF}" = "latest" ]; then \
        resolved_ref="$(curl -fsSL "https://api.github.com/repos/${DS_REPO}/releases/latest" | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n 1)"; \
    fi && \
    test -n "${resolved_ref}" && \
    ds_base="https://cdn.jsdelivr.net/gh/${DS_REPO}@${resolved_ref}/dist" && \
    curl -fsSL "${ds_base}/dzarlax.css" \
        -o internal/viz/static/assets/vendor/dzarlax.css && \
    curl -fsSL "${ds_base}/dzarlax.js" \
        -o internal/viz/static/assets/vendor/dzarlax.js && \
    curl -fsSL "https://unpkg.com/vis-network@${VIS_NETWORK_VERSION}/standalone/umd/vis-network.min.js" \
        -o internal/viz/static/assets/vendor/vis-network.min.js && \
    curl -fsSL "https://unpkg.com/vis-timeline@${VIS_TIMELINE_VERSION}/standalone/umd/vis-timeline-graph2d.min.js" \
        -o internal/viz/static/assets/vendor/vis-timeline-graph2d.min.js && \
    curl -fsSL "https://unpkg.com/vis-timeline@${VIS_TIMELINE_VERSION}/styles/vis-timeline-graph2d.min.css" \
        -o internal/viz/static/assets/vendor/vis-timeline-graph2d.min.css

RUN CGO_ENABLED=0 go build -o /personal-memory ./cmd/server
RUN CGO_ENABLED=0 go build -o /personal-memory-indexer ./cmd/indexer

FROM alpine:3.21
RUN apk add --no-cache ca-certificates
COPY --from=builder /personal-memory /personal-memory
COPY --from=builder /personal-memory-indexer /personal-memory-indexer

ENTRYPOINT ["/personal-memory"]
