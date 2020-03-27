PKG := github.com/lightninglabs/agora
ESCPKG := github.com\/lightninglabs\/agora

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

BTCD_COMMIT := $(shell cat go.mod | \
		grep $(BTCD_PKG) | \
		tail -n1 | \
		awk -F " " '{ print $$2 }' | \
		awk -F "/" '{ print $$1 }')

LND_COMMIT := $(shell cat go.mod | \
		grep $(LND_PKG) | \
		tail -n1 | \
		awk -F " " '{ print $$2 }' | \
		awk -F "/" '{ print $$1 }')

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

include make/testing_flags.mk

LINT = $(LINT_BIN) run -v

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

btcd:
	@$(call print, "Installing btcd.")
	$(DEPGET) $(BTCD_PKG)@$(BTCD_COMMIT)

lnd:
	@$(call print, "Installing lnd.")
	$(DEPGET) $(LND_PKG)@$(LND_COMMIT)

# ============
# INSTALLATION
# ============

build:
	@$(call print, "Building agora.")
	$(GOBUILD) $(PKG)/client/cmd/agora
	$(GOBUILD) $(PKG)/client/cmd/agorad
	$(GOBUILD) $(PKG)/cmd/agoraserver

build-itest:
	@$(call print, "Building itest lnd.")
	$(GOBUILD) -tags="$(ITEST_TAGS)" -o itest/lnd-itest $(LDFLAGS) $(LND_PKG)/cmd/lnd

install:
	@$(call print, "Installing agora.")
	$(GOINSTALL) $(PKG)/client/cmd/agora
	$(GOINSTALL) $(PKG)/client/cmd/agorad
	$(GOINSTALL) $(PKG)/cmd/agoraserver

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

travis-race: lint unit-race

travis-cover: lint unit-cover goveralls

travis-itest: lint

itest: btcd build-itest itest-only

itest-only:
	@$(call print, "Running integration tests with ${backend} backend.")
	$(ITEST)

# =============
# FLAKE HUNTING
# =============
flake-unit:
	@$(call print, "Flake hunting unit tests.")
	while [ $$? -eq 0 ]; do GOTRACEBACK=all $(UNIT) -count=1; done

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

rpc:
	@$(call print, "Compiling protos.")
	cd ./client/clmrpc; ./gen_protos.sh

clean:
	@$(call print, "Cleaning source.$(NC)")
	$(RM) ./agora
	$(RM) ./agorad
	$(RM) ./agoradserver
	$(RM) coverage.txt
