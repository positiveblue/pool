package subastadb

import (
	"context"
	"fmt"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// SQLGORMStore is the main object to communicate with the SQL db.
type SQLGORMStore struct {
	db *gorm.DB
}

// NewSQLGORMStore constructs a new SQLGORMStore.
func NewSQLGORMStore(cfg *SQLConfig) (*SQLGORMStore, error) {
	db, err := openPostgresDB(cfg)
	if err != nil {
		return nil, err
	}

	return &SQLGORMStore{db: db}, nil
}

// openPostgresDB opens a PostreSQL database and initializes the tables
// corresponding to the SQL models defined in this package.
func openPostgresDB(cfg *SQLConfig) (*gorm.DB, error) {
	sslMode := "disable"
	if cfg.RequireSSL {
		sslMode = "require"
	}

	dsn := fmt.Sprintf(
		"user=%v password=%v dbname=%v host=%v port=%v sslmode=%v",
		cfg.User, cfg.Password, cfg.DBName, cfg.Host, cfg.Port, sslMode,
	)

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		return nil, err
	}

	sqlDb, err := db.DB()
	if err != nil {
		return nil, err
	}

	var maxOpenConnections int
	if cfg.MaxOpenConnections != 0 {
		maxOpenConnections = cfg.MaxOpenConnections
	}
	sqlDb.SetMaxOpenConns(maxOpenConnections)

	if err := db.AutoMigrate(&SQLAskOrder{}); err != nil {
		return nil, err
	}

	if err := db.AutoMigrate(&SQLBidOrder{}); err != nil {
		return nil, err
	}

	if err := db.AutoMigrate(&SQLAccount{}); err != nil {
		return nil, err
	}

	if err := db.AutoMigrate(
		&SQLAccountDiff{}, &SQLMatchedOrder{}, &SQLBatchSnapshot{},
	); err != nil {
		return nil, err
	}

	if err := db.AutoMigrate(&SQLTraderTerms{}); err != nil {
		return nil, err
	}

	return db, nil
}

// SQLTransaction is a higher level abstraction around the ORM provided sql
// transaction.
type SQLTransaction struct {
	tx *gorm.DB
}

// Transaction starts and attempts to commit an SQL transaction.
func (s *SQLGORMStore) Transaction(ctx context.Context,
	apply func(tx *SQLTransaction) error) error {

	return s.db.WithContext(ctx).Transaction(func(dbTx *gorm.DB) error {
		sqlTx := &SQLTransaction{
			tx: dbTx,
		}
		return apply(sqlTx)
	})
}
