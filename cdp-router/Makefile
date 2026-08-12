.PHONY: proto openapi admin-deps admin-check admin-build admin-sync test e2e-test test-all check build

PROTO_FILES := $(shell find api -name '*.proto')
FRONTEND_DIR := ../web/admin
ADMIN_DIST := web/admin/dist
TEST_PKGS := $(shell go list ./... | grep -v '/tests/e2e$$')

proto:
	protoc -I . -I third_party \
		--go_out=. --go_opt=paths=source_relative \
		$(PROTO_FILES)

openapi:
	protoc -I . -I third_party \
		--openapi_out=openapi \
		$(PROTO_FILES)

admin-deps:
	@if [ -f "$(FRONTEND_DIR)/package.json" ]; then \
		if [ ! -d "$(FRONTEND_DIR)/node_modules" ]; then \
			(cd "$(FRONTEND_DIR)" && npm install); \
		fi; \
	fi

admin-check: admin-deps
	@if [ -f "$(FRONTEND_DIR)/package.json" ]; then \
		(cd "$(FRONTEND_DIR)" && npm run typecheck); \
	fi

admin-build: admin-deps
	@if [ -f "$(FRONTEND_DIR)/package.json" ]; then \
		(cd "$(FRONTEND_DIR)" && npm run build); \
	fi

admin-sync: admin-build
	@if [ -d "$(FRONTEND_DIR)/dist" ]; then \
		rm -rf "$(ADMIN_DIST)"; \
		mkdir -p "$(dir $(ADMIN_DIST))"; \
		cp -R "$(FRONTEND_DIR)/dist" "$(ADMIN_DIST)"; \
	fi

test:
	go test $(TEST_PKGS)

e2e-test:
	go test ./tests/e2e

test-all:
	go test ./...

check: proto openapi admin-build
	gofmt -w $$(find . -name '*.go' -not -path './web/admin/node_modules/*')
	go test $(TEST_PKGS)

build: proto openapi admin-sync
	go build -o bin/cdp-router ./cmd/cdp-router
