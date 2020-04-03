// +build itest

package itest

var testCases = []*testCase{
	{
		name: "master acct init",
		test: testMasterAcctInit,
	},
	{
		name: "create account",
		test: testAccountCreation,
	},
	{
		name: "create account subscription",
		test: testAccountSubscription,
	},
	{
		name: "submit order",
		test: testOrderSubmission,
	},
}
