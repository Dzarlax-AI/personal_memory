.PHONY: help dev-deps verify-assets build test vet eval-public conformance-public tidy clean

DS_VERSION ?= v1.4.0
DS_DIR := internal/viz/static/assets/vendor
ASSET_CHECKSUMS := build/browser-assets.sha256
VIS_NETWORK_VERSION ?= 9.1.9
VIS_TIMELINE_VERSION ?= 7.7.3
UNPKG_BASE := https://unpkg.com

help:
	@echo "Targets:"
	@echo "  make dev-deps  — fetch browser bundles into $(DS_DIR)"
	@echo "  make build     — build all five binaries (runs dev-deps first)"
	@echo "  make test      — verify assets + vet + test + build all binaries"
	@echo "  make eval-public — run the public retrieval dataset against Qdrant"
	@echo "  make conformance-public — run the public model-memory conformance suite"
	@echo "  make clean     — remove built binaries and the vendored browser bundles"

dev-deps:
	@mkdir -p $(DS_DIR)
	@ds_base="https://github.com/dzarlax/design-system/releases/download/$(DS_VERSION)"; \
	echo "Fetching design system from $$ds_base ..."; \
	curl -fsSL "$$ds_base/dzarlax.css" -o $(DS_DIR)/dzarlax.css; \
	curl -fsSL "$$ds_base/dzarlax.js"  -o $(DS_DIR)/dzarlax.js
	@echo "Fetching vis-network $(VIS_NETWORK_VERSION) and vis-timeline $(VIS_TIMELINE_VERSION) ..."
	@curl -fsSL "$(UNPKG_BASE)/vis-network@$(VIS_NETWORK_VERSION)/standalone/umd/vis-network.min.js" -o $(DS_DIR)/vis-network.min.js
	@curl -fsSL "$(UNPKG_BASE)/vis-timeline@$(VIS_TIMELINE_VERSION)/standalone/umd/vis-timeline-graph2d.min.js" -o $(DS_DIR)/vis-timeline-graph2d.min.js
	@curl -fsSL "$(UNPKG_BASE)/vis-timeline@$(VIS_TIMELINE_VERSION)/styles/vis-timeline-graph2d.min.css" -o $(DS_DIR)/vis-timeline-graph2d.min.css
	@$(MAKE) verify-assets
	@echo "OK — bundle at $(DS_DIR)/"

verify-assets:
	@if command -v sha256sum >/dev/null 2>&1; then \
		sha256sum -c $(ASSET_CHECKSUMS); \
	else \
		shasum -a 256 -c $(ASSET_CHECKSUMS); \
	fi

build: dev-deps
	go build ./cmd/server ./cmd/indexer ./cmd/migrate-memory-ids ./cmd/migrate-memory-lifecycle ./cmd/eval-memory ./cmd/conformance-memory

vet:
	go vet ./...

test: dev-deps vet
	go test ./...
	go build ./cmd/server ./cmd/indexer ./cmd/migrate-memory-ids ./cmd/migrate-memory-lifecycle ./cmd/eval-memory ./cmd/conformance-memory

QDRANT_TEST_URL ?= http://127.0.0.1:6333

eval-public:
	mkdir -p eval-results
	go run ./cmd/eval-memory run \
		--source fixture \
		--dataset evaldata/public/v3/dataset.json \
		--qdrant-url $(QDRANT_TEST_URL) \
		--json eval-results/public-v3-baseline.json \
		--markdown eval-results/public-v3-baseline.md
	cmp evaldata/public/v3/baseline.json eval-results/public-v3-baseline.json
	go run ./cmd/eval-memory run \
		--source fixture \
		--dataset evaldata/public/v3/dataset.json \
		--qdrant-url $(QDRANT_TEST_URL) \
		--configuration-name public-v3-legacy-raw-hybrid-rrf60-candidate \
		--retrieval-strategy hybrid-rrf \
		--dense-candidate-limit 40 \
		--rrf-constant 60 \
		--json eval-results/public-v3-hybrid-rrf60.json \
		--markdown eval-results/public-v3-hybrid-rrf60.md
	cmp evaldata/public/v3/hybrid-rrf60-candidate.json eval-results/public-v3-hybrid-rrf60.json

conformance-public:
	go run ./cmd/conformance-memory run \
		--source fixture \
		--suite conformancedata/public/v1/scenarios.json \
		--contract docs/model-usage-contract.md \
		--traces conformancedata/public/v1/traces/passing.json \
		--json conformance-results/public.json \
		--markdown conformance-results/public.md

tidy:
	go mod tidy

clean:
	rm -rf $(DS_DIR) conformance-results /tmp/personal-memory /tmp/personal-memory-indexer /tmp/personal-memory-migrate-ids
