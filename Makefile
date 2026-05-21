.PHONY: help dev-deps build test vet tidy clean

DS_REPO ?= dzarlax/design-system
DS_REF ?= latest
DS_DIR := internal/viz/static/assets/vendor
VIS_NETWORK_VERSION ?= 9.1.9
VIS_TIMELINE_VERSION ?= 7.7.3
UNPKG_BASE := https://unpkg.com

help:
	@echo "Targets:"
	@echo "  make dev-deps  — fetch browser bundles into $(DS_DIR)"
	@echo "  make build     — go build both binaries (runs dev-deps first)"
	@echo "  make test      — go vet + go test"
	@echo "  make clean     — remove built binaries and the vendored browser bundles"

dev-deps:
	@mkdir -p $(DS_DIR)
	@resolved_ref="$(DS_REF)"; \
	if [ "$$resolved_ref" = "latest" ]; then \
		resolved_ref=$$(curl -fsSL "https://api.github.com/repos/$(DS_REPO)/releases/latest" | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n 1); \
	fi; \
	if [ -z "$$resolved_ref" ]; then echo "Could not resolve $(DS_REPO) release"; exit 1; fi; \
	ds_base="https://cdn.jsdelivr.net/gh/$(DS_REPO)@$$resolved_ref/dist"; \
	echo "Fetching design system from $$ds_base ..."; \
	curl -fsSL "$$ds_base/dzarlax.css" -o $(DS_DIR)/dzarlax.css; \
	curl -fsSL "$$ds_base/dzarlax.js"  -o $(DS_DIR)/dzarlax.js
	@echo "Fetching vis-network $(VIS_NETWORK_VERSION) and vis-timeline $(VIS_TIMELINE_VERSION) ..."
	@curl -fsSL "$(UNPKG_BASE)/vis-network@$(VIS_NETWORK_VERSION)/standalone/umd/vis-network.min.js" -o $(DS_DIR)/vis-network.min.js
	@curl -fsSL "$(UNPKG_BASE)/vis-timeline@$(VIS_TIMELINE_VERSION)/standalone/umd/vis-timeline-graph2d.min.js" -o $(DS_DIR)/vis-timeline-graph2d.min.js
	@curl -fsSL "$(UNPKG_BASE)/vis-timeline@$(VIS_TIMELINE_VERSION)/styles/vis-timeline-graph2d.min.css" -o $(DS_DIR)/vis-timeline-graph2d.min.css
	@echo "OK — bundle at $(DS_DIR)/"

build: dev-deps
	go build ./cmd/server ./cmd/indexer

vet:
	go vet ./...

test: dev-deps vet
	go test ./...

tidy:
	go mod tidy

clean:
	rm -rf $(DS_DIR) /tmp/personal-memory /tmp/personal-memory-indexer
