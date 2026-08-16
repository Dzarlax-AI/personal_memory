.PHONY: help dev-deps verify-assets build test vet eval-public eval-public-v4 conformance-public integration-bundle-public docs-install docs-check docs-build tidy clean

DS_VERSION ?= v1.4.0
DS_DIR := internal/viz/static/assets/vendor
ASSET_CHECKSUMS := build/browser-assets.sha256
VIS_NETWORK_VERSION ?= 9.1.9
VIS_TIMELINE_VERSION ?= 7.7.3
UNPKG_BASE := https://unpkg.com

help:
	@echo "Targets:"
	@echo "  make dev-deps  — fetch browser bundles into $(DS_DIR)"
	@echo "  make build     — build all eight binaries (runs dev-deps first)"
	@echo "  make test      — verify assets + vet + test + build all binaries"
	@echo "  make eval-public — run the public retrieval dataset against Qdrant"
	@echo "  make eval-public-v4 — replay public document-routing evidence"
	@echo "  make conformance-public — run the public model-memory conformance suite"
	@echo "  make integration-bundle-public — validate bundle artifacts against the public suite"
	@echo "  make docs-install — install documentation dependencies"
	@echo "  make docs-check — validate documentation"
	@echo "  make docs-build — build documentation"
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
	go build ./cmd/server ./cmd/indexer ./cmd/migrate-memory-ids ./cmd/migrate-memory-lifecycle ./cmd/eval-memory ./cmd/conformance-memory ./cmd/memory-integration ./cmd/maintenance

vet:
	go vet ./...

test: dev-deps vet
	go test ./...
	go build ./cmd/server ./cmd/indexer ./cmd/migrate-memory-ids ./cmd/migrate-memory-lifecycle ./cmd/eval-memory ./cmd/conformance-memory ./cmd/memory-integration ./cmd/maintenance

QDRANT_TEST_URL ?= http://127.0.0.1:6333
# eval-memory compare uses this distinct status for an expected gate rejection;
# status 1 remains reserved for usage, input, output, and other errors.
EVAL_GATE_FAILURE_EXIT := 3

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

eval-public-v4:
	mkdir -p eval-results
	rm -f eval-results/public-v4-*.json eval-results/public-v4-*.md eval-results/eval-memory-v4
	go build -o eval-results/eval-memory-v4 ./cmd/eval-memory
	@set -e; for strategy in hierarchical-only flat-only blended-rrf; do \
		eval-results/eval-memory-v4 run --source fixture \
			--dataset evaldata/public/v4/dataset.json --qdrant-url $(QDRANT_TEST_URL) \
			--configuration-name "public-v4-$$strategy" --document-routing-strategy "$$strategy" \
			--json "eval-results/public-v4-$$strategy.json" \
			--markdown "eval-results/public-v4-$$strategy.md"; \
		cmp "evaldata/public/v4/$$strategy.json" "eval-results/public-v4-$$strategy.json"; \
	done
	eval-results/eval-memory-v4 run --source fixture \
		--dataset evaldata/public/v4/dataset.json --qdrant-url $(QDRANT_TEST_URL) \
		--configuration-name public-v4-blended-rrf-reranker-unavailable-fail-open \
		--document-routing-strategy blended-rrf \
		--reranker-model-id Alibaba-NLP/gte-multilingual-reranker-base@unavailable \
		--reranker-candidate-cap 20 --reranker-timeout-ms 500 \
		--offline-reranker-unavailable \
		--json eval-results/public-v4-reranker-unavailable-fail-open.json \
		--markdown eval-results/public-v4-reranker-unavailable-fail-open.md
	cmp evaldata/public/v4/reranker-unavailable-fail-open.json eval-results/public-v4-reranker-unavailable-fail-open.json
	@set -e; for candidate in flat-only blended-rrf reranker-unavailable; do \
		case "$$candidate" in \
			flat-only) report=flat-only ;; \
			blended-rrf) report=blended-rrf ;; \
			*) report=reranker-unavailable-fail-open ;; \
		esac; \
		set +e; eval-results/eval-memory-v4 compare \
			--baseline eval-results/public-v4-hierarchical-only.json \
			--candidate "eval-results/public-v4-$$report.json" --enforce-gates \
			--json "eval-results/public-v4-$$candidate-comparison.json"; status=$$?; set -e; \
		test $$status -eq $(EVAL_GATE_FAILURE_EXIT); \
		cmp "evaldata/public/v4/$$candidate-failing-comparison.json" \
			"eval-results/public-v4-$$candidate-comparison.json"; \
	done

conformance-public:
	go run ./cmd/conformance-memory run \
		--source fixture \
		--suite conformancedata/public/v1/scenarios.json \
		--contract website/src/content/docs/reference/model-memory-usage-contract.md \
		--traces conformancedata/public/v1/traces/passing.json \
		--json conformance-results/public.json \
		--markdown conformance-results/public.md

integration-bundle-public:
	mkdir -p integration-results
	rm -f integration-results/*.json integration-results/*.md integration-results/memory-integration
	go build -o integration-results/memory-integration ./cmd/memory-integration
	@set -e; for client in codex claude chatgpt generic_mcp; do \
		go run ./cmd/conformance-memory run \
			--source live \
			--suite conformancedata/public/v1/scenarios.json \
			--contract website/src/content/docs/reference/model-memory-usage-contract.md \
			--client-family "$$client" \
			--adapter-exec "$(CURDIR)/integration-results/memory-integration" \
			--adapter-arg conformance-adapter \
			--adapter-arg=--contract-source \
			--adapter-arg "$(CURDIR)/website/src/content/docs/reference/model-memory-usage-contract.md" \
			--adapter-arg=--suite-source \
			--adapter-arg "$(CURDIR)/conformancedata/public/v1/scenarios.json" \
			--json "integration-results/$$client.json" \
			--markdown "integration-results/$$client.md"; \
	done

docs-install:
	cd website && npm ci

docs-check:
	cd website && ASTRO_TELEMETRY_DISABLED=1 npm run check

docs-build:
	cd website && ASTRO_TELEMETRY_DISABLED=1 npm run build

tidy:
	go mod tidy

clean:
	rm -rf $(DS_DIR) conformance-results integration-results /tmp/personal-memory /tmp/personal-memory-indexer /tmp/personal-memory-migrate-ids
