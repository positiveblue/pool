package chanenforcement

import (
	"testing"

	"github.com/lightningnetwork/lnd/chainntnfs"
	"github.com/lightningnetwork/lnd/chanbackup"
)

// TestPruneMatureLifetimePackage ensures that a spend after a channel's
// maturity height is not interpreted as a violation.
func TestPruneMatureLifetimePackage(t *testing.T) {
	t.Parallel()

	h := newTestHarness(t)
	h.start()
	defer h.stop()

	const confHeight = 100

	// Enforce a new channel's lifetime.
	pkg := h.newLifetimePackage(chanbackup.AnchorsCommitVersion)
	h.enforceChannelLifetimes(pkg)
	h.notifyConf(pkg.ChannelPoint.Hash, &chainntnfs.TxConfirmation{
		BlockHeight: confHeight,
	})

	// Notify that it has been spent after it has matured.
	h.notifyMatureSpend(pkg, confHeight)

	// There should not be a lifetime violation present for the channel.
	h.assertNoViolation(pkg)
}

// TestEnforceLifetimeViolation ensures that we properly detect a lifetime
// violation by either party of a channel for the different supported channel
// types.
func TestEnforceLifetimeViolation(t *testing.T) {
	t.Parallel()

	const confHeight = 100

	testCases := []struct {
		name            string
		version         chanbackup.SingleBackupVersion
		cooperative     bool
		missingToRemote bool

		// Whether the channel initiator is responsible for the violation.
		channelInitiator bool
	}{
		{
			name:             "cooperative close",
			version:          chanbackup.AnchorsCommitVersion,
			cooperative:      true,
			channelInitiator: false,
		},
		{
			name:             "anchors initiator offender",
			version:          chanbackup.AnchorsCommitVersion,
			channelInitiator: true,
		},
		{
			name:             "anchors non-initiator offender",
			version:          chanbackup.AnchorsCommitVersion,
			channelInitiator: false,
		},
		{
			name:             "anchors initiator offender without to_remote",
			version:          chanbackup.AnchorsCommitVersion,
			channelInitiator: true,
		},
		{
			name:             "tweakless initiator offender",
			version:          chanbackup.TweaklessCommitVersion,
			channelInitiator: true,
			missingToRemote:  true,
		},
		{
			name:             "tweakless non-initiator offender",
			version:          chanbackup.TweaklessCommitVersion,
			channelInitiator: false,
		},
		{
			name:             "tweakless initiator offender without to_remote",
			version:          chanbackup.TweaklessCommitVersion,
			channelInitiator: true,
			missingToRemote:  true,
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		success := t.Run(testCase.name, func(t *testing.T) {
			h := newTestHarness(t)
			h.start()
			defer h.stop()

			// Enforce a new channel's lifetime.
			pkg := h.newLifetimePackage(testCase.version)
			h.enforceChannelLifetimes(pkg)
			h.notifyConf(pkg.ChannelPoint.Hash, &chainntnfs.TxConfirmation{
				BlockHeight: confHeight,
			})

			// Notify a premature spend originating from the
			// expected test case's party.
			h.notifyPrematureSpend(
				pkg, confHeight, testCase.channelInitiator,
				testCase.cooperative, testCase.missingToRemote,
			)

			// A violation should be found if we were expecting one.
			if testCase.channelInitiator {
				h.assertViolation(pkg)
			} else {
				h.assertNoViolation(pkg)
			}
		})
		if !success {
			return
		}
	}

}

// TestResumeEnforcementAtStartup ensures that we can properly detect a lifetime
// violation after a restart.
func TestResumeEnforcementAtStartup(t *testing.T) {
	t.Parallel()

	const confHeight = 100

	h := newTestHarness(t)

	// Add two channel lifetime packages before starting our
	// ChannelEnforcer. This helps us simulate the restart flow.
	pkg1 := h.newLifetimePackage(chanbackup.AnchorsCommitVersion)
	pkg2 := h.newLifetimePackage(chanbackup.AnchorsCommitVersion)

	h.start()
	defer h.stop()

	// With the ChannelEnforcer started, confirm both packages.
	h.notifyConf(pkg1.ChannelPoint.Hash, &chainntnfs.TxConfirmation{
		BlockHeight: confHeight,
	})
	h.notifyConf(pkg2.ChannelPoint.Hash, &chainntnfs.TxConfirmation{
		BlockHeight: confHeight,
	})

	// We'll notify a premature spend for the first package, so we should
	// expect a violation.
	h.notifyPrematureSpend(pkg1, confHeight, true, false, false)
	h.assertViolation(pkg1)

	// For the second package, we'll notify a mature spend, so we should not
	// expect a violation.
	h.notifyMatureSpend(pkg2, confHeight)
	h.assertNoViolation(pkg2)
}
