//go:build itest
// +build itest

package itest

var testCases = []*testCase{
	{
		name:               "master acct init v0",
		test:               testMasterAcctInit,
		skipMasterAcctInit: true,
		useV0MasterAcct:    true,
	},
	{
		name:               "master acct init v1",
		test:               testMasterAcctInit,
		skipMasterAcctInit: true,
	},
	{
		name: "create account",
		test: testAccountCreation,
	},
	{
		name: "account withdrawal",
		test: testAccountWithdrawal,
	},
	{
		name: "account deposit",
		test: testAccountDeposit,
	},
	{
		name: "account renewal",
		test: testAccountRenewal,
	},
	{
		name: "create account subscription",
		test: testAccountSubscription,
	},
	{
		name: "submit order",
		test: testOrderSubmission,
	},
	// The following three tests need to be individual test cases (and not
	// just subtests) so we can start with a fresh auctioneer master
	// account.
	{
		name:            "batch execution account v0",
		test:            testBatchExecutionV0,
		useV0MasterAcct: true,
	},
	{
		name:            "batch execution account v1",
		test:            testBatchExecutionV1,
		useV0MasterAcct: true,
	},
	{
		name:            "batch execution account v0 upgrade to v1",
		test:            testBatchExecutionV0ToV1,
		useV0MasterAcct: true,
	},
	{
		name: "service level enforcement",
		test: testServiceLevelEnforcement,
	},
	{
		name: "script level enforcement",
		test: testScriptLevelEnforcement,
	},
	{
		name: "unconfirmed batch chain",
		test: testUnconfirmedBatchChain,
	},
	{
		name: "batch execution dust",
		test: testBatchExecutionDustOutputs,
	},
	{
		name: "server-assisted recover accounts",
		test: testServerAssistedAccountRecovery,
	},
	{
		name: "consecutive batch execution",
		test: testConsecutiveBatches,
	},
	{
		name: "batch partial reject new nodes only",
		test: testTraderPartialRejectNewNodesOnly,
	},
	{
		name: "batch partial reject funding failure",
		test: testTraderPartialRejectFundingFailure,
	},
	{
		name: "manual batch fee bump",
		test: testManualFeeBump,
	},
	{
		name: "node rating agency and matching",
		test: testNodeRatingAgencyAndMatching,
	},
	{
		name: "batch matching conditions",
		test: testBatchMatchingConditions,
	},
	{
		name: "distinct lease duration buckets",
		test: testBatchExecutionDurationBuckets,
	},
	{
		name: "batch extra inputs outputs",
		test: testBatchIO,
	},
	{
		name: "self channel balance",
		test: testSelfChanBalance,
	},
	{
		name: "sidecar channels happy path",
		test: testSidecarChannelsHappyPath,
	},
	{
		name: "sidecar channels reject new nodes only",
		test: testSidecarChannelsRejectNewNodesOnly,
	},
	{
		name: "sidecar channels reject min chan size",
		test: testSidecarChannelsRejectMinChanSize,
	},
	{
		name: "sidecar channels cancellation",
		test: testSidecarTicketCancellation,
	},
	{
		name: "sidecar channels with offline participant",
		test: sidecarChannelsRecipientOffline,
	},
	{
		name: "custom execution fee",
		test: testCustomExecutionFee,
	},
	{
		name: "batch account auto-renewal",
		test: testBatchAccountAutoRenewal,
	},
	{
		name: "hashmail server",
		test: testHashMailServer,
	},
}
