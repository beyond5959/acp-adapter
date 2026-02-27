GO ?= go

.PHONY: test schema stress-j1

test:
	$(GO) test ./...

schema:
	mkdir -p internal/appserver/schema
	codex app-server generate-json-schema --out internal/appserver/schema

stress-j1:
	RUN_STRESS_J1=1 $(GO) test ./test/integration -run TestE2EAcceptanceJ1Stress100Turns -count=1
