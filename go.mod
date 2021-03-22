module github.com/lightninglabs/subasta

require (
	github.com/btcsuite/btcd v0.21.0-beta.0.20201208033208-6bd4c64a54fa
	github.com/btcsuite/btclog v0.0.0-20170628155309-84c8d2346e9f
	github.com/btcsuite/btcutil v1.0.2
	github.com/btcsuite/btcwallet/wtxmgr v1.2.0
	github.com/davecgh/go-spew v1.1.1
	github.com/go-errors/errors v1.0.1
	github.com/golang/protobuf v1.4.3
	github.com/grpc-ecosystem/go-grpc-prometheus v1.2.0
	github.com/grpc-ecosystem/grpc-gateway v1.14.6
	github.com/jessevdk/go-flags v1.4.0
	github.com/lightninglabs/aperture v0.1.5-beta
	github.com/lightninglabs/lndclient v0.12.0-3
	github.com/lightninglabs/pool v0.4.4-alpha.0.20210316143314-9fb2862fede4
	github.com/lightninglabs/pool/auctioneerrpc v1.0.1
	github.com/lightninglabs/protobuf-hex-display v1.4.3-hex-display
	github.com/lightningnetwork/lnd v0.12.0-beta
	github.com/lightningnetwork/lnd/cert v1.0.3
	github.com/lightningnetwork/lnd/ticker v1.0.0
	github.com/prometheus/client_golang v1.5.1
	github.com/stretchr/testify v1.5.1
	github.com/urfave/cli v1.20.0
	go.etcd.io/etcd v3.3.22+incompatible
	google.golang.org/grpc v1.29.1
	google.golang.org/protobuf v1.25.0
	gopkg.in/macaroon.v2 v2.1.0
)

go 1.13

// Fix incompatibility of etcd go.mod package.
// See https://github.com/etcd-io/etcd/issues/11154
replace go.etcd.io/etcd => go.etcd.io/etcd v0.0.0-20200520232829-54ba9589114f

// The subasta/auctioneerrpc package declares itself as pool/auctioneerrpc as
// well so go mod can identify it as the same package and allows us to replace
// it in the client binary as well. We need to import it with its declared name
// everywhere too, otherwise the replace won't work properly.
replace github.com/lightninglabs/pool/auctioneerrpc => ./auctioneerrpc
