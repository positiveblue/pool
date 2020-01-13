module github.com/lightninglabs/agora

require (
	github.com/btcsuite/btcd v0.20.1-beta
	github.com/btcsuite/btclog v0.0.0-20170628155309-84c8d2346e9f
	github.com/btcsuite/btcutil v0.0.0-20190425235716-9e5f4b9a998d
	github.com/coreos/bbolt v1.3.3
	github.com/coreos/etcd v3.3.17+incompatible
	github.com/davecgh/go-spew v1.1.1
	github.com/go-errors/errors v1.0.1
	github.com/golang/protobuf v1.3.2
	github.com/grpc-ecosystem/grpc-gateway v1.10.0
	github.com/jessevdk/go-flags v1.4.0
	github.com/lightninglabs/kirin v0.0.0-20200107133748-8b731e87e4b8
	github.com/lightninglabs/loop v0.3.0-alpha.0.20200108113330-d9597e838793
	github.com/lightninglabs/protobuf-hex-display v1.3.3-0.20191212020323-b444784ce75d
	github.com/lightningnetwork/lnd v0.8.0-beta-rc3.0.20200108005919-658803f51c05
	github.com/lightningnetwork/lnd/cert v1.0.0
	github.com/urfave/cli v1.20.0
	golang.org/x/crypto v0.0.0-20190829043050-9756ffdc2472
	google.golang.org/genproto v0.0.0-20191108220845-16a3f7862a1a
	google.golang.org/grpc v1.25.1
)

go 1.13
