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
}
