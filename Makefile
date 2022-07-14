PKG := github.com/lightninglabs/subasta
ESCPKG := github.com\/lightninglabs\/subasta

TOOLS_DIR := tools

BTCD_PKG := github.com/btcsuite/btcd
LND_PKG := github.com/lightningnetwork/lnd
GOACC_PKG := github.com/ory/go-acc
GOIMPORTS_PKG := github.com/rinchsan/gosimports/cmd/gosimports
GARBLE_PKG := mvdan.cc/garble

GO_BIN := ${GOPATH}/bin
LND_BIN := $(GO_BIN)/lnd
LINT_BIN := $(GO_BIN)/golangci-lint
GOACC_BIN := $(GO_BIN)/go-acc
GOIMPORTS_BIN := $(GO_BIN)/gosimports
GARBLE_BIN := $(GO_BIN)/garble

GOBUILD := go build -v
GOINSTALL := go install -v
GOTEST := go test -v
GARBLEBUILD := $(GARBLE_BIN) build -v
GARBLETEST := $(GARBLE_BIN) test -v

GOFILES_NOVENDOR = $(shell find . -type f -name '*.go' -not -path "./vendor/*" -not -name "*pb.go" -not -name "*pb.gw.go" -not -name "*.pb.json.go")
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

DOCKER_TOOLS = docker run -v $$(pwd):/build subasta-tools

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
$(GOACC_BIN):
	@$(call print, "Installing go-acc.")
	cd $(TOOLS_DIR); go install -trimpath $(GOACC_PKG)

$(GOIMPORTS_BIN):
	@$(call print, "Installing goimports.")
	cd $(TOOLS_DIR); go install -trimpath $(GOIMPORTS_PKG)

$(GARBLE_BIN):
	@$(call print, "Installing garble.")
	cd $(TOOLS_DIR); go install -trimpath $(GARBLE_PKG)

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

regtest-build: $(GARBLE_BIN)
	@$(call print, "Building stripped down regtest only binary of auction server.")
	$(GARBLEBUILD) -trimpath -ldflags="$(REGTEST_LDFLAGS)" -tags="regtest" -o auctionserver-regtest $(PKG)/cmd/auctionserver
	$(GARBLEBUILD) -ldflags="$(REGTEST_LDFLAGS)" -tags="regtest" -o auctioncli-regtest $(PKG)/cmd/auctioncli

docker-tools:
	@$(call print, "Building tools docker image.")
	docker build -q -t subasta-tools $(TOOLS_DIR)

scratch: build

# ===================
# DATABASE MIGRATIONS
# ===================

migrate-up: $(MIGRATE_BIN)
	migrate -path subastadb/postgres/migrations -database $(SUBASTA_DB_CONNECTIONSTRING) -verbose up

migrate-down: $(MIGRATE_BIN)
	migrate -path subastadb/postgres/migrations -database $(SUBASTA_DB_CONNECTIONSTRING) -verbose down 1

migrate-drop: $(MIGRATE_BIN)
	migrate -path subastadb/postgres/migrations -database $(SUBASTA_DB_CONNECTIONSTRING) -verbose drop

migrate-create: $(MIGRATE_BIN)
	migrate create -dir subastadb/postgres/migrations -seq -ext sql $(patchname)

# =======
# TESTING
# =======

check: unit

unit:
	@$(call print, "Running unit tests.")
	$(UNIT)

unit-sql:
	@$(call print, "Running unit tests with SQL database.")
	$(UNIT_SQL)

unit-cover: $(GOACC_BIN)
	@$(call print, "Running unit coverage tests.")
	$(GOACC_BIN) $(COVER_PKG)

unit-race:
	@$(call print, "Running unit race tests.")
	env CGO_ENABLED=1 GORACE="history_size=7 halt_on_errors=1" $(UNIT_RACE)

itest: build-itest itest-only

itest-only: aperture-dir
	@$(call print, "Running integration tests with ${backend} backend.")
	rm -rf itest/regtest; date
	$(GOTEST) ./itest -tags="$(ITEST_TAGS)" $(TEST_FLAGS) -logoutput -goroutinedump -btcdexec=./btcd-itest -logdir=regtest

itest-garble: $(GARBLE_BIN) build-itest aperture-dir
	@$(call print, "Running integration tests with ${backend} backend.")
	rm -rf itest/regtest; date
	$(GARBLETEST) -c -o itest/subasta-itest -tags="$(ITEST_TAGS)" ./itest
	cd itest; ./subasta-itest -test.v $(TEST_FLAGS) -logoutput -goroutinedump -btcdexec=./btcd-itest -logdir=regtest

itest-sql: build-itest aperture-dir
	@$(call print, "Running integration tests with ${backend} backend and SQL database.")
	rm -rf itest/regtest; date
	$(GOTEST) ./itest -tags="$(ITEST_TAGS)" $(TEST_FLAGS) -logoutput -goroutinedump -btcdexec=./btcd-itest -logdir=regtest -usesql=true

aperture-dir:
ifeq ($(UNAME_S),Linux)
	mkdir -p $$HOME/.aperture
endif
ifeq ($(UNAME_S),Darwin)
	mkdir -p "$$HOME/Library/Application Support/Aperture"
endif

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
fmt: $(GOIMPORTS_BIN)
	@$(call print, "Fixing imports.")
	gosimports -w $(GOFILES_NOVENDOR)
	@$(call print, "Formatting source.")
	gofmt -l -w -s $(GOFILES_NOVENDOR)

lint: docker-tools
	@$(call print, "Linting source.")
	$(DOCKER_TOOLS) golangci-lint run -v $(LINT_WORKERS)

list:
	@$(call print, "Listing commands.")
	@$(MAKE) -qp | \
		awk -F':' '/^[a-zA-Z0-9][^$$#\/\t=]*:([^=]|$$)/ {split($$1,A,/ /);for(i in A)print A[i]}' | \
		grep -v Makefile | \
		sort

gen: rpc mock sqlc

sqlc:
	@$(call print, "Generating sql models and queries in Go")
	cd ./gen; ./gen_sqlc_docker.sh

sqlc-check: sqlc
	@$(call print, "Verifying sql code generation.")
	if test -n "$$(git status --porcelain '*.go')"; then echo "SQL models not properly generated!"; git status --porcelain '*.go'; exit 1; fi

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
