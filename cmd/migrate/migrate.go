package main

import (
	"context"
	"fmt"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/lightninglabs/subasta/order"
	"github.com/lightninglabs/subasta/subastadb"
)

const (
	// dbTimeout is the default value for timout contexts. Leave enough
	// time to ensure that we do not timeout during the migration.
	dbTimeout = 60 * time.Minute
)

type migrateCommand struct {
	cfg *Config
}

func (x *migrateCommand) NewStore(source string) (Store, error) {
	ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
	defer cancel()

	switch source {
	case SQLDB:
		store, err := subastadb.NewSQLStore(ctx, x.cfg.SQL)
		if err != nil {
			return nil, err
		}

		err = store.Init(ctx)
		if err != nil {
			return nil, err
		}

		return store, nil

	case ETCDDB:
		activeNet := chaincfg.Params{Name: x.cfg.Network}
		store, err := subastadb.NewEtcdStore(
			activeNet, x.cfg.Etcd.Host, x.cfg.Etcd.User,
			x.cfg.Etcd.Password,
		)
		if err != nil {
			return nil, err
		}

		err = store.Init(ctx)
		if err != nil {
			return nil, err
		}

		return store, nil

	default:
		return nil, fmt.Errorf("unknown db")
	}
}

func (x *migrateCommand) Execute(_ []string) error {
	if x.cfg.From != ETCDDB && x.cfg.From != SQLDB {
		return fmt.Errorf("unknown db source: %s", x.cfg.From)
	} else if x.cfg.To != ETCDDB && x.cfg.To != SQLDB {
		return fmt.Errorf("unknown db destination: %s", x.cfg.To)
	}

	src, err := x.NewStore(x.cfg.From)
	if err != nil {
		return err
	}

	dst, err := x.NewStore(x.cfg.To)
	if err != nil {
		return err
	}

	fmt.Printf("Starting db migration (%v => %v) %v\n", x.cfg.From,
		x.cfg.To, time.Now())
	return migrateData(src, dst)
}

func migrateData(src, dst Store) error {
	if err := migrateAuctioneerAccount(src, dst); err != nil {
		return err
	}

	if err := migrateAccounts(src, dst); err != nil {
		return err
	}

	if err := migrateBans(src, dst); err != nil {
		return err
	}

	if err := migrateLifetimePackages(src, dst); err != nil {
		return err
	}

	if err := migrateLeaseDurations(src, dst); err != nil {
		return err
	}

	if err := migrateNodeRatings(src, dst); err != nil {
		return err
	}

	if err := migrateTraderTerms(src, dst); err != nil {
		return err
	}

	if err := migrateOrders(src, dst); err != nil {
		return err
	}

	if err := migrateBatches(src, dst); err != nil {
		return err
	}

	return nil
}

func migrateAuctioneerAccount(src, dst Store) error {
	fmt.Println("Migrating auctioneer account...")
	start := time.Now()

	ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
	defer cancel()
	auctioneer, err := src.FetchAuctioneerAccount(ctx)
	if err != nil {
		return err
	}

	err = dst.UpdateAuctioneerAccount(ctx, auctioneer)
	if err != nil {
		return err
	}

	err = dst.SetCurrentBatch(ctx, auctioneer.BatchKey)
	if err != nil {
		return err
	}

	elapsed := time.Since(start)
	fmt.Printf("Auctioneer migration finished in %v\n", elapsed)
	return nil
}

func migrateAccounts(src, dst Store) error {
	fmt.Println("Migrating reservations...")
	start := time.Now()

	ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
	defer cancel()
	reservations, tokens, err := src.Reservations(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("#reservations: %d\n", len(reservations))
	for idx := range reservations {
		err := dst.ReserveAccount(ctx, tokens[idx], reservations[idx])
		if err != nil {
			return err
		}
	}
	elapsed := time.Since(start)
	fmt.Printf("Account Reservations migration finished in %v\n", elapsed)

	fmt.Println("Migrating accounts...")
	start = time.Now()

	ctx, cancel = context.WithTimeout(context.Background(), dbTimeout)
	defer cancel()
	accounts, err := src.Accounts(ctx)
	if err != nil {
		return err
	}

	fmt.Printf("#accounts: %d\n", len(accounts))
	for _, account := range accounts {
		err := dst.StoreAccount(ctx, account)
		if err != nil {
			return err
		}
	}

	elapsed = time.Since(start)
	fmt.Printf("Accounts migration finished in %v\n", elapsed)

	fmt.Println("Migrating account diffs...")
	start = time.Now()

	ctx, cancel = context.WithTimeout(context.Background(), dbTimeout)
	defer cancel()
	diffs, err := src.ListAccountDiffs(ctx)
	if err != nil {
		return err
	}

	fmt.Printf("#account diffs: %d\n", len(diffs))
	for _, diff := range diffs {
		err := dst.CreateAccountDiff(ctx, diff)
		if err != nil {
			return err
		}
	}

	elapsed = time.Since(start)
	fmt.Printf("Account diffs migration finished in %v\n", elapsed)

	return nil
}

func migrateBans(src, dst Store) error {
	// Use a current height old enough so we get all the bans.
	currentHeight := uint32(103)

	fmt.Println("Migrating account bans...")
	start := time.Now()

	ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
	defer cancel()
	accBans, err := src.ListBannedAccounts(ctx, currentHeight)
	if err != nil {
		return nil
	}

	fmt.Printf("#account bans: %d\n", len(accBans))
	for keyBytes, ban := range accBans {
		accKey, err := btcec.ParsePubKey(keyBytes[:])
		if err != nil {
			return err
		}

		err = dst.BanAccount(ctx, accKey, ban)
		if err != nil {
			return err
		}
	}

	elapsed := time.Since(start)
	fmt.Printf("Account bans migration finished in %v\n", elapsed)

	fmt.Println("Migrating node bans...")
	start = time.Now()

	ctx, cancel = context.WithTimeout(context.Background(), dbTimeout)
	defer cancel()
	nodeBans, err := src.ListBannedNodes(ctx, currentHeight)
	if err != nil {
		return nil
	}

	fmt.Printf("#node bans: %d\n", len(nodeBans))
	for keyBytes, ban := range nodeBans {
		accKey, err := btcec.ParsePubKey(keyBytes[:])
		if err != nil {
			return err
		}

		err = dst.BanNode(ctx, accKey, ban)
		if err != nil {
			return err
		}
	}

	elapsed = time.Since(start)
	fmt.Printf("Node bans migration finished in %v\n", elapsed)

	return nil
}

func migrateLifetimePackages(src, dst Store) error {
	fmt.Println("Migrating lifetime packages...")
	start := time.Now()

	ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
	defer cancel()
	pkgs, err := src.LifetimePackages(ctx)
	if err != nil {
		return err
	}

	fmt.Printf("#lifetime packages: %d\n", len(pkgs))
	for _, pkg := range pkgs {
		err := dst.StoreLifetimePackage(ctx, pkg)
		if err != nil {
			return err
		}
	}

	elapsed := time.Since(start)
	fmt.Printf("Lifetime pkgs migration finished in %v\n", elapsed)

	return nil
}

func migrateLeaseDurations(src, dst Store) error {
	fmt.Println("Migrating lease durations...")
	start := time.Now()

	ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
	defer cancel()
	leaseDurations, err := src.LeaseDurations(ctx)
	if err != nil {
		return err
	}

	fmt.Printf("#lease durations: %d\n", len(leaseDurations))
	for duration, state := range leaseDurations {
		err := dst.StoreLeaseDuration(ctx, duration, state)
		if err != nil {
			return err
		}
	}

	elapsed := time.Since(start)
	fmt.Printf("Lease durations migration finished in %v\n", elapsed)
	return nil
}

func migrateNodeRatings(src, dst Store) error {
	fmt.Println("Migrating node ratings...")
	start := time.Now()

	ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
	defer cancel()
	ratings, err := src.NodeRatings(ctx)
	if err != nil {
		return err
	}

	fmt.Printf("#node ratings: %d\n", len(ratings))
	for nodeKey, tier := range ratings {
		err := dst.ModifyNodeRating(ctx, nodeKey, tier)
		if err != nil {
			return err
		}
	}

	elapsed := time.Since(start)
	fmt.Printf("Node ratings migration finished in %v\n", elapsed)

	return nil
}

func migrateTraderTerms(src, dst Store) error {
	fmt.Println("Migrating trader terms...")
	start := time.Now()

	ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
	defer cancel()
	terms, err := src.AllTraderTerms(ctx)
	if err != nil {
		return err
	}

	fmt.Printf("#trader terms: %d\n", len(terms))
	for _, term := range terms {
		err := dst.PutTraderTerms(ctx, term)
		if err != nil {
			return err
		}
	}

	elapsed := time.Since(start)
	fmt.Printf("Trader terms migration finished in %v\n", elapsed)

	return nil
}

func migrateOrders(src, dst Store) error {
	fmt.Println("Migrating orders...")
	start := time.Now()

	ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
	defer cancel()
	activeOrders, err := src.GetOrders(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("#active orders: %d\n", len(activeOrders))
	archivedOrders, err := src.GetArchivedOrders(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("#archived orders: %d\n", len(archivedOrders))

	totalNumOrders := len(activeOrders) + len(archivedOrders)
	orders := make([]order.ServerOrder, 0, totalNumOrders)
	orders = append(orders, activeOrders...)
	orders = append(orders, archivedOrders...)
	for _, order := range orders {
		err := dst.SubmitOrder(ctx, order)
		if err != nil {
			return err
		}

		if _, ok := dst.(*subastadb.EtcdStore); ok {
			err := dst.UpdateOrder(ctx, order.Nonce())
			if err != nil {
				return err
			}
		}
	}

	elapsed := time.Since(start)
	fmt.Printf("Orders migration finished in %v\n", elapsed)

	return nil
}

func migrateBatches(src, dst Store) error {
	fmt.Println("Migrating batches...")
	start := time.Now()

	ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
	defer cancel()
	batches, err := src.Batches(ctx)
	if err != nil {
		return err
	}

	fmt.Printf("#batches: %d\n", len(batches))
	for batchID, batch := range batches {
		confirmed, err := src.BatchConfirmed(ctx, batchID)
		if err != nil {
			return err
		}
		err = dst.StoreBatch(ctx, batchID, batch, confirmed)
		if err != nil {
			return err
		}
	}

	elapsed := time.Since(start)
	fmt.Printf("Batch migration finished (%d) in %v\n", len(batches),
		elapsed)

	return nil
}
