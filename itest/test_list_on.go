// +build itest

package itest

var testCases = []*testCase{
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
