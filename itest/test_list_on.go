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
		name: "server-assisted recover accounts",
		test: testServerAssistedAccountRecovery,
	},
}
