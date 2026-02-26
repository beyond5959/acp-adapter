GO ?= go

.PHONY: test schema

test:
	$(GO) test ./...

schema:
	mkdir -p internal/appserver/schema
	codex app-server generate-json-schema --out internal/appserver/schema
