module github.com/lightninglabs/subasta

require (
	github.com/btcsuite/btcd v0.20.1-beta.0.20200730232343-1db1b6f8217f
	github.com/btcsuite/btclog v0.0.0-20170628155309-84c8d2346e9f
	github.com/btcsuite/btcutil v1.0.2
	github.com/btcsuite/btcwallet/wtxmgr v1.2.0
	github.com/davecgh/go-spew v1.1.1
	github.com/go-errors/errors v1.0.1
	github.com/golang/protobuf v1.3.3
	github.com/grpc-ecosystem/go-grpc-prometheus v1.2.0
	github.com/jessevdk/go-flags v1.4.0
	github.com/lightninglabs/aperture v0.1.1-beta.0.20200901205500-5237b07a6ef5
	github.com/lightninglabs/lndclient v1.0.1-0.20201005192720-d7e439329144
	github.com/lightninglabs/pool v0.2.6-alpha.0.20201009230240-0f42edccaebf
	github.com/lightninglabs/protobuf-hex-display v1.3.3-0.20191212020323-b444784ce75d
	github.com/lightningnetwork/lnd v0.11.1-beta.rc3
	github.com/lightningnetwork/lnd/cert v1.0.3
	github.com/lightningnetwork/lnd/ticker v1.0.0
	github.com/prometheus/client_golang v1.5.1
	github.com/stretchr/testify v1.5.1
	github.com/urfave/cli v1.20.0
	go.etcd.io/etcd v3.3.22+incompatible
	golang.org/x/crypto v0.0.0-20200709230013-948cd5f35899
	google.golang.org/grpc v1.29.1
	gopkg.in/macaroon.v2 v2.1.0
)

go 1.13

// Fix incompatibility of etcd go.mod package.
// See https://github.com/etcd-io/etcd/issues/11154
replace go.etcd.io/etcd => go.etcd.io/etcd v0.0.0-20200520232829-54ba9589114f
