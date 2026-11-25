BUF := go run github.com/bufbuild/buf/cmd/buf@v1.75.0

.PHONY: all gen web build test notices
all: gen web build

OPENAPI = $(BUF) generate --template '{"version":"v2","managed":{"enabled":true,"disable":[{"file_option":"go_package","module":"buf.build/googleapis/googleapis"}],"override":[{"file_option":"go_package_prefix","value":"github.com/Alex4386/inaba/gen"}]},"plugins":[{"local":["go","tool","protoc-gen-openapi"],"out":"gen/openapi","opt":["fq_schema_naming=false","naming=proto","title=inaba API","version=v1"],"strategy":"all"}]}'

gen:
	$(BUF) dep update
	$(BUF) generate
	# One OpenAPI document for the whole REST API, with the hosts' operations moved to
	# where the server serves them: /v1/hosts/{host_id}/...
	$(OPENAPI)
	go run ./internal/tools/openapihost gen/openapi/openapi.yaml

web:
	cd web && pnpm install && pnpm build

build:
	go build -o bin/ ./cmd/...

# The licenses of what the binaries carry; run it after changing dependencies.
notices:
	node scripts/notices.mjs

test:
	go test ./...
