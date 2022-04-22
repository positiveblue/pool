module github.com/lightninglabs/subasta

require (
	github.com/btcsuite/btcd v0.22.0-beta.0.20220316175102-8d5c75c28923
	github.com/btcsuite/btcd/btcec/v2 v2.1.3
	github.com/btcsuite/btcd/btcutil v1.1.1
	github.com/btcsuite/btcd/chaincfg/chainhash v1.0.1
	github.com/btcsuite/btclog v0.0.0-20170628155309-84c8d2346e9f
	github.com/btcsuite/btcutil v0.0.0-20190425235716-9e5f4b9a998d
	github.com/btcsuite/btcwallet/wtxmgr v1.5.0
	github.com/davecgh/go-spew v1.1.1
	github.com/fortytw2/leaktest v1.3.0
	github.com/go-errors/errors v1.0.1
	github.com/golang/protobuf v1.5.2
	github.com/grpc-ecosystem/go-grpc-prometheus v1.2.0
	github.com/grpc-ecosystem/grpc-gateway v1.16.0
	github.com/jessevdk/go-flags v1.4.0
	github.com/lib/pq v1.10.3
	github.com/lightninglabs/aperture v0.1.17-beta.0.20220328072456-4a2632d0be38
	github.com/lightninglabs/faraday v0.2.7-alpha.0.20220328064033-d602082a718a
	github.com/lightninglabs/lndclient v0.15.0-2
	github.com/lightninglabs/pool v0.5.6-alpha.0.20220422072641-0c062755a09e
	github.com/lightninglabs/pool/auctioneerrpc v1.0.6-0.20220407153640-53b437d9b8c3
	github.com/lightninglabs/protobuf-hex-display v1.4.3-hex-display
	github.com/lightningnetwork/lnd v0.14.1-beta.0.20220330161355-8072b20d548d
	github.com/lightningnetwork/lnd/cert v1.1.1
	github.com/lightningnetwork/lnd/kvdb v1.3.1
	github.com/lightningnetwork/lnd/ticker v1.1.0
	github.com/lightningnetwork/lnd/tlv v1.0.2
	github.com/lightningnetwork/lnd/tor v1.0.0
	github.com/ory/dockertest/v3 v3.8.0
	github.com/prometheus/client_golang v1.11.0
	github.com/shopspring/decimal v1.2.0
	github.com/stretchr/testify v1.7.0
	github.com/urfave/cli v1.22.4
	go.etcd.io/etcd/client/v3 v3.5.1
	go.etcd.io/etcd/server/v3 v3.5.1
	golang.org/x/time v0.0.0-20210220033141-f8bda1e9f3ba
	google.golang.org/grpc v1.39.0
	google.golang.org/protobuf v1.27.1
	gopkg.in/macaroon.v2 v2.1.0
	gorm.io/driver/postgres v1.1.2
	gorm.io/gorm v1.21.15
)

go 1.16

// The subasta/auctioneerrpc package declares itself as pool/auctioneerrpc as
// well so go mod can identify it as the same package and allows us to replace
// it in the client binary as well. We need to import it with its declared name
// everywhere too, otherwise the replace won't work properly.
replace github.com/lightninglabs/pool/auctioneerrpc => ./auctioneerrpc

// Fix etcd token renewal issue https://github.com/etcd-io/etcd/pull/13262.
replace go.etcd.io/etcd/client/v3 => github.com/lightninglabs/etcd/client/v3 v3.5.1-retry-patch
