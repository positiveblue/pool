package main

import "github.com/lightninglabs/subasta/subastadb"

const (
	ETCDDB = "etcd"
	SQLDB  = "sql"
)

// EtcdConfig holds the etcd database configuration.
type EtcdConfig struct {
	Host     string `long:"host" description:"etcd instance address"`
	User     string `long:"user" description:"etcd user name"`
	Password string `long:"password" description:"etcd password"`
}

type Config struct {
	From    string `long:"from"  description:"source database"`
	To      string `long:"to"  description:"destination database"`
	Network string `long:"network" description:"bitcoin network"`

	Etcd *EtcdConfig          `group:"etcd" namespace:"etcd"`
	SQL  *subastadb.SQLConfig `group:"sql" namespace:"sql"`
}

// DefaultConfig returns the default config for a migration tool.
func DefaultConfig() *Config {
	return &Config{
		From:    ETCDDB,
		To:      SQLDB,
		Network: "mainnet",
		Etcd: &EtcdConfig{
			Host: "localhost:2379",
		},
		SQL: &subastadb.SQLConfig{
			Host:     "localhost",
			Port:     5432,
			User:     "pool",
			Password: "pool",
			DBName:   "pool",
		},
	}
}
