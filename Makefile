PKG := github.com/lightninglabs/subasta
ESCPKG := github.com\/lightninglabs\/subasta

BTCD_PKG := github.com/btcsuite/btcd
LND_PKG := github.com/lightningnetwork/lnd
LINT_PKG := github.com/golangci/golangci-lint/cmd/golangci-lint
GOVERALLS_PKG := github.com/mattn/goveralls
GOACC_PKG := github.com/ory/go-acc

GO_BIN := ${GOPATH}/bin
BTCD_BIN := $(GO_BIN)/btcd
LND_BIN := $(GO_BIN)/lnd
GOVERALLS_BIN := $(GO_BIN)/goveralls
LINT_BIN := $(GO_BIN)/golangci-lint
GOACC_BIN := $(GO_BIN)/go-acc

BTCD_DIR :=${GOPATH}/src/$(BTCD_PKG)
LND_DIR := ${GOPATH}/src/$(LND_PKG)

LINT_COMMIT := v1.18.0
GOACC_COMMIT := ddc355013f90fea78d83d3a6c71f1d37ac07ecd5

DEPGET := cd /tmp && go get -v
GOBUILD := go build -v
GOINSTALL := go install -v
GOTEST := go test -v

GOFILES_NOVENDOR = $(shell find . -type f -name '*.go' -not -path "./vendor/*")
GOLIST := go list -deps $(PKG)/... | grep '$(PKG)'| grep -v '/vendor/'
GOLISTCOVER := $(shell go list -deps -f '{{.ImportPath}}' ./... | grep '$(PKG)' | sed -e 's/^$(ESCPKG)/./')

RM := rm -f
CP := cp
MAKE := make
XARGS := xargs -L 1
UNAME_S := $(shell uname -s)

include make/testing_flags.mk

# Linting uses a lot of memory, so keep it under control by limiting the number
# of workers if requested.
ifneq ($(workers),)
LINT_WORKERS = --concurrency=$(workers)
endif

make_ldflags = -ldflags "-X $(LND_PKG)/build.RawTags=$(shell echo $(1) | sed -e 's/ /,/g')"
LND_ITEST_LDFLAGS := $(call make_ldflags, $(ITEST_TAGS))

# Remove as much debug information as possible with the -s (remove symbol table)
# and the -w (omit DWARF symbol table) flags.
REGTEST_LDFLAGS = -s -w -buildid= -X $(PKG).Commit=regtest

LINT = $(LINT_BIN) run -v $(LINT_WORKERS)

GREEN := "\\033[0;32m"
NC := "\\033[0m"
define print
	echo $(GREEN)$1$(NC)
endef

default: scratch

all: scratch check install

# ============
# DEPENDENCIES
# ============

$(GOVERALLS_BIN):
	@$(call print, "Fetching goveralls.")
	go get -u $(GOVERALLS_PKG)

$(LINT_BIN):
	@$(call print, "Fetching linter")
	$(DEPGET) $(LINT_PKG)@$(LINT_COMMIT)

$(GOACC_BIN):
	@$(call print, "Fetching go-acc")
	$(DEPGET) $(GOACC_PKG)@$(GOACC_COMMIT)

# ============
# INSTALLATION
# ============

build:
	@$(call print, "Building auction server and cli.")
	$(GOBUILD) -tags=kvdb_etcd $(PKG)/cmd/auctionserver
	$(GOBUILD) $(PKG)/cmd/auctioncli

build-itest:
	@$(call print, "Building itest btcd.")
	CGO_ENABLED=0 $(GOBUILD) -tags="rpctest" -o itest/btcd-itest $(BTCD_PKG)

	@$(call print, "Building itest lnd.")
	CGO_ENABLED=0 $(GOBUILD) -tags="$(ITEST_TAGS)" -o itest/lnd-itest $(LND_ITEST_LDFLAGS) $(LND_PKG)/cmd/lnd

install:
	@$(call print, "Installing auction server and cli.")
	$(GOINSTALL) $(PKG)/cmd/auctionserver
	$(GOINSTALL) $(PKG)/cmd/auctioncli

regtest-build:
	@$(call print, "Building stripped down regtest only binary of auction server.")
	$(GOBUILD) -trimpath -ldflags="$(REGTEST_LDFLAGS)" -tags="regtest" -o auctionserver-regtest $(PKG)/cmd/auctionserver
	$(GOBUILD) -trimpath -ldflags="$(REGTEST_LDFLAGS)" -tags="regtest" -o auctioncli-regtest $(PKG)/cmd/auctioncli

scratch: build

# =======
# TESTING
# =======

check: unit

unit:
	@$(call print, "Running unit tests.")
	$(UNIT)

unit-cover: $(GOACC_BIN)
	@$(call print, "Running unit coverage tests.")
	$(GOACC_BIN) $(COVER_PKG)

unit-race:
	@$(call print, "Running unit race tests.")
	env CGO_ENABLED=1 GORACE="history_size=7 halt_on_errors=1" $(UNIT_RACE)

goveralls: $(GOVERALLS_BIN)
	@$(call print, "Sending coverage report.")
	$(GOVERALLS_BIN) -coverprofile=coverage.txt -service=travis-ci

itest: build-itest itest-only

itest-only:
	@$(call print, "Running integration tests with ${backend} backend.")
ifeq ($(UNAME_S),Linux)
	mkdir -p $$HOME/.aperture
endif
ifeq ($(UNAME_S),Darwin)
	mkdir -p "$$HOME/Library/Application Support/Aperture"
endif
	rm -rf itest/regtest; date
	$(GOTEST) ./itest -tags="$(ITEST_TAGS)" $(TEST_FLAGS) -logoutput -goroutinedump -btcdexec=./btcd-itest -logdir=regtest

# =============
# FLAKE HUNTING
# =============
flake-unit:
	@$(call print, "Flake hunting unit tests.")
	while [ $$? -eq 0 ]; do GOTRACEBACK=all $(UNIT) -count=1; done

flake-race:
	@$(call print, "Flake hunting race tests.")
	while [ $$? -eq 0 ]; do GOTRACEBACK=all CGO_ENABLED=1 GORACE="history_size=7 halt_on_errors=1" $(UNIT_RACE) -count=1; done

flakehunt:
	@$(call print, "Flake hunting itests.")
	while [ $$? -eq 0 ]; do GOTRACEBACK=all $(ITEST); done

# =========
# UTILITIES
# =========
fmt:
	@$(call print, "Formatting source.")
	gofmt -l -w -s $(GOFILES_NOVENDOR)

lint: $(LINT_BIN)
	@$(call print, "Linting source.")
	$(LINT)

list:
	@$(call print, "Listing commands.")
	@$(MAKE) -qp | \
		awk -F':' '/^[a-zA-Z0-9][^$$#\/\t=]*:([^=]|$$)/ {split($$1,A,/ /);for(i in A)print A[i]}' | \
		grep -v Makefile | \
		sort

gen: rpc mock

mock:
	@$(call print, "Generating mock packages.")
	cd ./gen; ./gen_mock_docker.sh

mock-check: mock
	@$(call print, "Verifying mocks.")
	if test -n "$$(git status --porcelain '*.go')"; then echo "Mocks not properly generated!"; git status --porcelain '*.go'; exit 1; fi

rpc:
	@$(call print, "Compiling protos.")
	cd ./auctioneerrpc; ./gen_protos_docker.sh

rpc-check: rpc
	@$(call print, "Verifying protos.")
	if test -n "$$(git describe --dirty | grep dirty)"; then echo "Protos not properly formatted or not compiled with correct version!"; git status; git diff; exit 1; fi

clean:
	@$(call print, "Cleaning source.$(NC)")
	$(RM) ./auctionserver
	$(RM) ./auctioncli
	$(RM) coverage.txt
