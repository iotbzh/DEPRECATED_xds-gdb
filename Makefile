# Makefile used to build xds-gdb commands

# Application Version
VERSION := 0.1.0


# Retrieve git tag/commit to set sub-version string
ifeq ($(origin SUB_VERSION), undefined)
	SUB_VERSION := $(shell git describe --exact-match --tags 2>/dev/null | sed 's/^v//')
	ifneq ($(SUB_VERSION), )
		VERSION := $(firstword $(subst -, ,$(SUB_VERSION)))
		SUB_VERSION := $(word 2,$(subst -, ,$(SUB_VERSION)))
	endif
	ifeq ($(SUB_VERSION), )
		SUB_VERSION := $(shell git rev-parse --short HEAD)
		ifeq ($(SUB_VERSION), )
			SUB_VERSION := unknown-dev
		endif
	endif
endif

HOST_GOOS=$(shell go env GOOS)
HOST_GOARCH=$(shell go env GOARCH)
ARCH=$(HOST_GOOS)-$(HOST_GOARCH)
REPOPATH=github.com/iotbzh/xds-gdb

EXT=
ifeq ($(HOST_GOOS), windows)
	EXT=.exe
endif


mkfile_path := $(abspath $(lastword $(MAKEFILE_LIST)))
ROOT_SRCDIR := $(patsubst %/,%,$(dir $(mkfile_path)))
BINDIR := $(ROOT_SRCDIR)/bin
ROOT_GOPRJ := $(abspath $(ROOT_SRCDIR)/../../../..)
PACKAGE_DIR := $(ROOT_SRCDIR)/package

export GOPATH := $(shell go env GOPATH):$(ROOT_GOPRJ)
export PATH := $(PATH):$(ROOT_SRCDIR)/tools

VERBOSE_1 := -v
VERBOSE_2 := -v -x

# Release or Debug mode
ifeq ($(filter 1,$(RELEASE) $(REL)),)
	GORELEASE=
	BUILD_MODE="Debug mode"
else
	# optimized code without debug info
	GORELEASE= -s -w
	BUILD_MODE="Release mode"
endif


.PHONY: all
all: build

.PHONY: build
build: vendor
	@echo "### Build $@ (version $(VERSION), subversion $(SUB_VERSION)) - $(BUILD_MODE)";
	@cd $(ROOT_SRCDIR); $(BUILD_ENV_FLAGS) go build $(VERBOSE_$(V)) -i -o $(BINDIR)/$@$(EXT) -ldflags "$(GORELEASE) -X main.AppVersion=$(VERSION) -X main.AppSubVersion=$(SUB_VERSION)" .

test: tools/glide
	go test --race $(shell ./tools/glide novendor)

vet: tools/glide
	go vet $(shell ./tools/glide novendor)

fmt: tools/glide
	go fmt $(shell ./tools/glide novendor)

.PHONY: clean
clean:
	rm -rf $(BINDIR)/* debug $(ROOT_GOPRJ)/pkg/*/$(REPOPATH) $(PACKAGE_DIR)

distclean: clean
	rm -rf $(BINDIR) tools glide.lock vendor $(ROOT_SRCDIR)/*.zip

.PHONY: release
release:
	RELEASE=1 make -f $(ROOT_SRCDIR)/Makefile clean build

package: clean build
	@mkdir -p $(PACKAGE_DIR)/xds-gdb
	@cp -a $(BINDIR)/*gdb$(EXT) $(PACKAGE_DIR)/xds-gdb
	@cd $(PACKAGE_DIR) && zip  --symlinks -r $(ROOT_SRCDIR)/xds-gdb_$(ARCH)-v$(VERSION)_$(SUB_VERSION).zip ./xds-gdb

.PHONY: package-all
package-all:
	@echo "# Build linux amd64..."
	GOOS=linux GOARCH=amd64 RELEASE=1 make -f $(ROOT_SRCDIR)/Makefile package
	@echo "# Build windows amd64..."
#	GOOS=windows GOARCH=amd64 RELEASE=1 make -f $(ROOT_SRCDIR)/Makefile package
	@echo " WARNING: build on Windows not supported for now."
	@echo "# Build darwin amd64..."
	GOOS=darwin GOARCH=amd64 RELEASE=1 make -f $(ROOT_SRCDIR)/Makefile package
	make -f $(ROOT_SRCDIR)/Makefile clean

vendor: tools/glide glide.yaml
	./tools/glide install --strip-vendor

vendor/debug: vendor
	(cd vendor/github.com/iotbzh && \
		rm -rf xds-common && ln -s ../../../../xds-common && \
		rm -rf xds-server && ln -s ../../../../xds-server )

tools/glide:
	@echo "Downloading glide"
	mkdir -p tools
	curl --silent -L https://glide.sh/get | GOBIN=./tools  sh

help:
	@echo "Main supported rules:"
	@echo "  all               (default)"
	@echo "  release"
	@echo "  clean"
	@echo "  package"
	@echo "  distclean"
	@echo ""
	@echo "Influential make variables:"
	@echo "  V                 - Build verbosity {0,1,2}."
	@echo "  BUILD_ENV_FLAGS   - Environment added to 'go build'."
