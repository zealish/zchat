GO ?= go
GOBIN := $(shell $(GO) env GOPATH)/bin
UI_DIR := apps/desktop/ui
BUILD_DIR := build
DIST_DIR := dist
APP_ID := com.zealish.ZChat
VERSION ?= 0.5.0
GO_LDFLAGS ?=
GO_BUILD_FLAGS := $(if $(GO_LDFLAGS),-ldflags '$(GO_LDFLAGS)',)

PROTOC_GEN_GO_VERSION := v1.36.12
PROTOC_GEN_GO_GRPC_VERSION := v1.6.2

.PHONY: all proto ui daemon desktop build run test vet clean reset-session dist-tarball rpm flatpak

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
	CGO_ENABLED=0 $(GO) build $(GO_BUILD_FLAGS) -o $(BUILD_DIR)/zchat-daemon ./apps/daemon

desktop: ui
	CGO_ENABLED=1 $(GO) build $(GO_BUILD_FLAGS) -o $(BUILD_DIR)/zchat ./apps/desktop

build: daemon desktop

run: build
	ZCHAT_DAEMON=./$(BUILD_DIR)/zchat-daemon ./$(BUILD_DIR)/zchat

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

clean:
	rm -rf $(BUILD_DIR) $(UI_DIR)/window.ui $(UI_DIR)/zchat.gresource

# dist-tarball produces the vendored source archive both packaging paths build
# from, so neither needs network access at build time. The tree is listed via
# git so ignored build output stays out, but the files come from the working
# copy rather than HEAD.
dist-tarball:
	rm -rf $(DIST_DIR)/zchat-$(VERSION)
	mkdir -p $(DIST_DIR)/zchat-$(VERSION)
	git ls-files -z --cached --others --exclude-standard \
		| tar --null -T - -c -f - | tar -x -C $(DIST_DIR)/zchat-$(VERSION)
	cd $(DIST_DIR)/zchat-$(VERSION) && $(GO) mod vendor
	tar -czf $(DIST_DIR)/zchat-$(VERSION).tar.gz -C $(DIST_DIR) zchat-$(VERSION)
	rm -rf $(DIST_DIR)/zchat-$(VERSION)

rpm: dist-tarball
	mkdir -p $(DIST_DIR)/rpmbuild/SOURCES
	cp $(DIST_DIR)/zchat-$(VERSION).tar.gz $(DIST_DIR)/rpmbuild/SOURCES/
	rpmbuild --define "_topdir $(CURDIR)/$(DIST_DIR)/rpmbuild" \
		--define "_zchat_version $(VERSION)" \
		-bb packaging/rpm/zchat.spec
	find $(DIST_DIR)/rpmbuild/RPMS -name '*.rpm' -exec cp {} $(DIST_DIR)/ \;

# The manifest cannot interpolate make variables, so the versioned tarball is
# copied to a stable name the flatpak source can point at.
flatpak: dist-tarball
	cp $(DIST_DIR)/zchat-$(VERSION).tar.gz $(DIST_DIR)/zchat-src.tar.gz
	flatpak-builder --force-clean --repo=$(DIST_DIR)/flatpak-repo \
		$(DIST_DIR)/flatpak-build packaging/flatpak/$(APP_ID).yaml
	flatpak build-bundle $(DIST_DIR)/flatpak-repo \
		$(DIST_DIR)/zchat-$(VERSION).flatpak $(APP_ID)

reset-session:
	rm -f $$HOME/.local/share/zchat/zchat.db*
