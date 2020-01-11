package clientdb

import (
	"io/ioutil"
	"os"
	"reflect"
	"testing"

	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcutil"
	"github.com/davecgh/go-spew/spew"
	"github.com/lightninglabs/agora/client/account"
	"github.com/lightninglabs/agora/client/clmscript"
	"github.com/lightningnetwork/lnd/keychain"
)

var (
	testOutPoint = wire.OutPoint{Index: 1}
)

func newTestDB(t *testing.T) (*DB, func()) {
	tempDir, err := ioutil.TempDir("", "client-db")
	if err != nil {
		t.Fatalf("unable to create temp dir: %v", err)
	}

	db, err := New(tempDir)
	if err != nil {
		os.RemoveAll(tempDir)
		t.Fatalf("unable to create new db: %v", err)
	}

	return db, func() {
		db.Close()
		os.RemoveAll(tempDir)
	}
}

func assertAccountExists(t *testing.T, db *DB, expected *account.Account) {
	t.Helper()

	found, err := db.Account(expected.TraderKey)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(found, expected) {
		t.Fatalf("expected account: %v\ngot: %v", spew.Sdump(expected),
			spew.Sdump(found))
	}
}

// TestAccounts ensures that all database operations involving accounts run as
// expected.
func TestAccounts(t *testing.T) {
	t.Parallel()

	db, cleanup := newTestDB(t)
	defer cleanup()

	// Create a test account we'll use to interact with the database.
	a := &account.Account{
		Value:     btcutil.SatoshiPerBitcoin,
		Expiry:    1337,
		TraderKey: [33]byte{1},
		TraderKeyLocator: keychain.KeyLocator{
			Family: clmscript.AccountKeyFamily,
			Index:  1,
		},
		AuctioneerKey: [33]byte{2},
		State:         account.StateInitiated,
		HeightHint:    1,
	}

	// First, we'll add it to the database. We should be able to retrieve
	// after.
	if err := db.AddAccount(a); err != nil {
		t.Fatalf("unable to add account: %v", err)
	}
	assertAccountExists(t, db, a)

	// Transition the account from StateInitiated to StatePendingOpen. If
	// the database update is successful, the in-memory account should be
	// updated as well.
	err := db.UpdateAccount(
		a, account.StateModifier(account.StatePendingOpen),
		account.OutPointModifier(testOutPoint),
	)
	if err != nil {
		t.Fatalf("unable to update account: %v", err)
	}
	assertAccountExists(t, db, a)

	// Retrieving all accounts should show that we only have one account,
	// the same one.
	accounts, err := db.Accounts()
	if err != nil {
		t.Fatalf("unable to retrieve accounts: %v", err)
	}
	if len(accounts) != 1 {
		t.Fatalf("expected 1 account, found %v", len(accounts))
	}
	if !reflect.DeepEqual(accounts[0], a) {
		t.Fatalf("expected account: %v\ngot: %v", spew.Sdump(a),
			spew.Sdump(accounts[0]))
	}
}
