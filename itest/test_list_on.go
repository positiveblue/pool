// +build itest

package itest

var testCases = []*testCase{
	{
		name:               "master acct init",
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
	{
		name: "batch execution",
		test: testBatchExecution,
	},
	{
		name: "service level enforcement",
		test: testServiceLevelEnforcement,
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
		name: "batch sponsor",
		test: testBatchSponsor,
	},
}
