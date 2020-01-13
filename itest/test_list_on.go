// +build itest

package itest

var testCases = []*testCase{
	{
		name: "create account",
		test: testAccountCreation,
	},
	{
		name: "submit order",
		test: testOrderSubmission,
	},
}
