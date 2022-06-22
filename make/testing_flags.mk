TEST_FLAGS =
COVER_PKG = $$(go list -deps ./... | grep '$(PKG)')

# If specific package is being unit tested, construct the full name of the
# subpackage.
ifneq ($(pkg),)
UNITPKG := $(PKG)/$(pkg)
UNIT_TARGETED = yes
COVER_PKG = $(PKG)/$(pkg)
endif

# If a specific unit test case is being target, construct test.run filter.
ifneq ($(case),)
TEST_FLAGS += -test.run=$(case)
UNIT_TARGETED = yes
endif

# Define the integration test.run filter if the icase argument was provided.
ifneq ($(icase),)
TEST_FLAGS += -test.run=TestAuctioneerServer/$(icase)
endif

# If a timeout was requested, construct initialize the proper flag for the go
# test command. If not, we set 20m (up from the default 10m).
ifneq ($(timeout),)
TEST_FLAGS += -test.timeout=$(timeout)
else
TEST_FLAGS += -test.timeout=20m
endif

# UNIT_TARGTED is undefined iff a specific package and/or unit test case is
# not being targeted.
UNIT_TARGETED ?= no

# If a specific package/test case was requested, run the unit test for the
# targeted case. Otherwise, default to running all tests.
ifeq ($(UNIT_TARGETED), yes)
UNIT := $(GOTEST) $(TEST_FLAGS) $(UNITPKG)
UNIT_SQL := $(GOTEST) -tags=sql $(TEST_FLAGS) $(UNITPKG)
UNIT_RACE := $(GOTEST) $(TEST_FLAGS) -race $(UNITPKG)
endif

ifeq ($(UNIT_TARGETED), no)
UNIT := $(GOLIST) | $(XARGS) env $(GOTEST) $(TEST_FLAGS)
UNIT_SQL := $(GOLIST) | $(XARGS) env $(GOTEST) -tags=sql $(TEST_FLAGS)
UNIT_RACE := $(UNIT) -race
endif

# Default to btcd backend if not set.
ifeq ($(backend),)
backend = btcd
endif

# Construct the integration test command with the added build flags.
ITEST_TAGS := itest dev rpctest kvdb_etcd chainrpc walletrpc signrpc invoicesrpc autopilotrpc routerrpc watchtowerrpc wtclientrpc $(backend)
