GO ?= go

.PHONY: test schema schema-check stress-j1 e2e-real

test:
	$(GO) test ./...

schema:
	mkdir -p internal/appserver/schema
	codex app-server generate-json-schema --out internal/appserver/schema
	$(MAKE) schema-check

schema-check:
	@files=$$(find internal/appserver/schema -type f ! -name README.md ! -name SHA256SUMS | sort); \
	if [ -z "$$files" ]; then \
		echo "schema check failed: no generated schema files found"; \
		exit 1; \
	fi; \
	for f in $$files; do \
		if [ ! -s "$$f" ]; then \
			echo "schema check failed: empty file $$f"; \
			exit 1; \
		fi; \
	done; \
	if command -v shasum >/dev/null 2>&1; then \
		echo "$$files" | xargs shasum -a 256 > internal/appserver/schema/SHA256SUMS; \
	elif command -v sha256sum >/dev/null 2>&1; then \
		echo "$$files" | xargs sha256sum > internal/appserver/schema/SHA256SUMS; \
	else \
		echo "schema check failed: neither shasum nor sha256sum found"; \
		exit 1; \
	fi

stress-j1:
	RUN_STRESS_J1=1 $(GO) test ./test/integration -run TestE2EAcceptanceJ1Stress100Turns -count=1

e2e-real: schema
	E2E_REAL_CODEX=1 $(GO) test ./... -run TestE2E -count=1
