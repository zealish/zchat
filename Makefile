GO ?= go
GOBIN := $(shell $(GO) env GOPATH)/bin
UI_DIR := apps/desktop/ui
BUILD_DIR := build

PROTOC_GEN_GO_VERSION := v1.36.12
PROTOC_GEN_GO_GRPC_VERSION := v1.6.2

.PHONY: all proto ui daemon desktop build run test vet clean reset-session

all: build

proto:
	$(GO) install google.golang.org/protobuf/cmd/protoc-gen-go@$(PROTOC_GEN_GO_VERSION)
	$(GO) install google.golang.org/grpc/cmd/protoc-gen-go-grpc@$(PROTOC_GEN_GO_GRPC_VERSION)
	PATH="$(GOBIN):$$PATH" protoc --proto_path=proto \
		--go_out=. --go_opt=module=github.com/zealish/zchat \
		--go-grpc_out=. --go-grpc_opt=module=github.com/zealish/zchat \
		proto/zchat/v1/zchat.proto

ui:
	blueprint-compiler compile $(UI_DIR)/window.blp --output $(UI_DIR)/window.ui
	glib-compile-resources --sourcedir=$(UI_DIR) --target=$(UI_DIR)/zchat.gresource $(UI_DIR)/zchat.gresource.xml

daemon:
	CGO_ENABLED=0 $(GO) build -o $(BUILD_DIR)/zchat-daemon ./apps/daemon

desktop: ui
	CGO_ENABLED=1 $(GO) build -o $(BUILD_DIR)/zchat ./apps/desktop

build: daemon desktop

run: build
	ZCHAT_DAEMON=./$(BUILD_DIR)/zchat-daemon ./$(BUILD_DIR)/zchat

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

clean:
	rm -rf $(BUILD_DIR) $(UI_DIR)/window.ui $(UI_DIR)/zchat.gresource

reset-session:
	rm -f $$HOME/.local/share/zchat/zchat.db*
