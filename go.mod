module github.com/lightninglabs/subasta

require (
	github.com/btcsuite/btcd v0.22.0-beta.0.20211005184431-e3449998be39
	github.com/btcsuite/btclog v0.0.0-20170628155309-84c8d2346e9f
	github.com/btcsuite/btcutil v1.0.3-0.20210527170813-e2ba6805a890
	github.com/btcsuite/btcwallet/wtxmgr v1.3.1-0.20210822222949-9b5a201c344c
	github.com/davecgh/go-spew v1.1.1
	github.com/go-errors/errors v1.0.1
	github.com/golang/protobuf v1.5.2
	github.com/grpc-ecosystem/go-grpc-prometheus v1.2.0
	github.com/grpc-ecosystem/grpc-gateway v1.16.0
	github.com/jessevdk/go-flags v1.4.0
	github.com/lib/pq v1.10.3
	github.com/lightninglabs/aperture v0.1.10-beta
	github.com/lightninglabs/lndclient v0.14.0-7
	github.com/lightninglabs/pool v0.5.4-alpha
	github.com/lightninglabs/pool/auctioneerrpc v1.0.5
	github.com/lightninglabs/protobuf-hex-display v1.4.3-hex-display
	github.com/lightningnetwork/lnd v0.14.1-beta
	github.com/lightningnetwork/lnd/cert v1.1.0
	github.com/lightningnetwork/lnd/ticker v1.1.0
	github.com/ory/dockertest/v3 v3.8.0
	github.com/prometheus/client_golang v1.11.0
	github.com/stretchr/testify v1.7.0
	github.com/urfave/cli v1.22.1
	go.etcd.io/etcd/client/v3 v3.5.0
	go.etcd.io/etcd/server/v3 v3.5.0
	golang.org/x/time v0.0.0-20210220033141-f8bda1e9f3ba
	google.golang.org/grpc v1.38.0
	google.golang.org/protobuf v1.26.0
	gopkg.in/macaroon.v2 v2.1.0
	gorm.io/driver/postgres v1.1.2
	gorm.io/gorm v1.21.15
)

go 1.13

// The subasta/auctioneerrpc package declares itself as pool/auctioneerrpc as
// well so go mod can identify it as the same package and allows us to replace
// it in the client binary as well. We need to import it with its declared name
// everywhere too, otherwise the replace won't work properly.
replace github.com/lightninglabs/pool/auctioneerrpc => ./auctioneerrpc
