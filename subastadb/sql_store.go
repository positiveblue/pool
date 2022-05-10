package subastadb

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/golang-migrate/migrate/v4"
	pgx_migrate "github.com/golang-migrate/migrate/v4/database/pgx"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/golang-migrate/migrate/v4/source/httpfs"
	"github.com/jackc/pgx/v4"
	"github.com/jackc/pgx/v4/pgxpool"
	orderT "github.com/lightninglabs/pool/order"
	"github.com/lightninglabs/subasta/order"
	"github.com/lightninglabs/subasta/subastadb/postgres"
)

const (
	dsnTemplate = "postgres://%v:%v@%v:%d/%v?sslmode=%v"
)

var (
	//go:embed postgres/migrations/*.up.sql
	sqlSchemas embed.FS

	// migrateSync ensures that the migrations are run only once.
	migrateSync sync.Once
)

// SQLConfig holds database configuration.
type SQLConfig struct {
	Host               string `long:"host" description:"Database server hostname."`
	Port               int    `long:"port" description:"Database server port."`
	User               string `long:"user" description:"Database user."`
	Password           string `long:"password" description:"Database user's password."`
	DBName             string `long:"dbname" description:"Database name to use."`
	MaxOpenConnections int    `long:"maxconnections" description:"Max open connections to keep alive to the database server."`
	RequireSSL         bool   `long:"requiressl" description:"Whether to require using SSL (mode: require) when connecting to the server."`
}

// DSN returns the dns to connect to the database.
func (s *SQLConfig) DSN(hidePassword bool) string {
	var sslMode = "disable"
	if s.RequireSSL {
		sslMode = "require"
	}

	password := s.Password
	if hidePassword {
		// Placeholder used for logging the DSN safely.
		password = "****"
	}

	return fmt.Sprintf(dsnTemplate, s.User, password, s.Host, s.Port,
		s.DBName, sslMode)
}

// SQLStore is the main object to communicate with the SQL db.
type SQLStore struct {
	cfg     *SQLConfig
	db      *pgxpool.Pool
	queries *postgres.Queries
}

// Make sure that SQLStore implements the Store/AdminStore interfaces.
var _ Store = (*SQLStore)(nil)
var _ AdminStore = (*SQLStore)(nil)

// MirrorToSQL attempts to mirror accounts, orders and batches to the configured
// SQL database.
//
// TODO(positiveblue): Delete when sql migration is completed.
func (s *SQLStore) MirrorToSQL(ctx context.Context) error {
	return nil
}

// NewSQLStore constructs a new SQLStore.
func NewSQLStore(ctx context.Context, cfg *SQLConfig) (*SQLStore, error) {
	log.Infof("Using SQL database '%s'", cfg.DSN(true))

	db, err := pgxpool.Connect(ctx, cfg.DSN(false))
	if err != nil {
		return nil, err
	}

	queries := postgres.New(db)
	return &SQLStore{
		cfg:     cfg,
		db:      db,
		queries: queries,
	}, nil
}

// RunMigrations applies the latest sql migrations from the embedded files in
// the Go binary.
func (s *SQLStore) RunMigrations(ctx context.Context) error {
	// Create a specific connection for running the migrations.
	// SQLStore uses pgxpool while pgx_migrates asks for a `sql.DB`.
	db, err := sql.Open("pgx", s.cfg.DSN(false))
	if err != nil {
		return err
	}

	// Close the db connection after we are done.
	defer func() {
		if err := db.Close(); err != nil {
			log.Errorf("unable to close db after running "+
				"migrations: %v", err)
		}
	}()

	// Now that the database is open, populate the database with
	// our set of schemas based on our embedded in-memory file
	// system.
	//
	// First, we'll need to open up a new migration instance for
	// our current target database: pgx(postgres).
	driver, err := pgx_migrate.WithInstance(db, &pgx_migrate.Config{})
	if err != nil {
		return err
	}

	// With the migrate instance open, we'll create a new migration
	// source using the embedded file system stored in sqlSchemas.
	// The library we're using can't handle a raw file system
	// interface, so we wrap it in this intermediate layer.
	migrateFileServer, err := httpfs.New(
		http.FS(sqlSchemas), "postgres/migrations",
	)
	if err != nil {
		return err
	}
	defer migrateFileServer.Close()

	// Finally, we'll run the migration with our driver above based
	// on the open DB, and also the migration source stored in the
	// file system above.
	sqlMigrate, err := migrate.NewWithInstance(
		"migrations", migrateFileServer, "postgres", driver,
	)
	if err != nil {
		return err
	}

	version, dirty, err := sqlMigrate.Version()
	switch {
	case err != nil && err != migrate.ErrNilVersion:
		return err
	case dirty:
		return fmt.Errorf("unable to execute db migration: db is in " +
			"a dirty state")
	}

	log.Infof("Current DB version: %v", version)

	start := time.Now()
	err = sqlMigrate.Up()
	if err != nil && err != migrate.ErrNoChange {
		return err
	}

	// TODO(positiveblue): send event data to prometheus.
	log.Infof("Executing DB migrations took %v", time.Since(start))

	version, _, err = sqlMigrate.Version()
	if err != nil && err != migrate.ErrNilVersion {
		return err
	}

	log.Infof("DB version after running migrations: %v", version)
	return nil
}

// Init initializes the necessary versioning state if the database hasn't
// already been created in the past.
func (s *SQLStore) Init(ctx context.Context) error {
	var err error
	migrateSync.Do(func() {
		// TODO(positiveblue): run migrations in a different app/pod.
		err = s.RunMigrations(ctx)
	})
	if err != nil {
		return err
	}

	_, err = s.queries.GetCurrentBatchKey(ctx)
	if err == pgx.ErrNoRows {
		return s.insertDefaultValues(ctx)
	}

	return err
}

// insertDefaultValues adds default values to the database where needed.
func (s *SQLStore) insertDefaultValues(ctx context.Context) error {
	// Insert batchKey.
	currentBatchKeyParams := postgres.UpsertCurrentBatchKeyParams{
		BatchKey: InitialBatchKey.SerializeCompressed(),
	}
	err := s.queries.UpsertCurrentBatchKey(ctx, currentBatchKeyParams)
	if err != nil {
		return err
	}

	// Insert default lease durations.
	duration := orderT.LegacyLeaseDurationBucket
	marketState := order.BucketStateClearingMarket

	params := postgres.UpsertLeaseDurationParams{
		Duration: int64(duration),
		State:    int16(marketState),
	}
	return s.queries.UpsertLeaseDuration(ctx, params)
}

// ExecTx is a wrapper for txBody to abstract the creation and commit of a db
// transaction. The db transaction is embedded in a `*postgres.Queries` that
// txBody needs to use when executing each one of the queries that need to be
// applied atomically.
func (s *SQLStore) ExecTx(ctx context.Context,
	txBody func(*postgres.Queries) error) error {

	// Create the db transaction.
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}

	// Rollback is safe to call even if the tx is already closed, so if
	// the tx commits successfully, this is a no-op.
	defer func() {
		err := tx.Rollback(ctx)
		switch {
		// If the tx was already closed (it was successfully executed)
		// we do not need to log that error.
		case err == pgx.ErrTxClosed:
			return

		// If this is an unexpected error, log it.
		case err != nil:
			log.Errorf("unable to rollback db tx: %v", err)
		}
	}()

	if err := txBody(s.queries.WithTx(tx)); err != nil {
		return err
	}

	// Commit transaction.
	return tx.Commit(ctx)
}
