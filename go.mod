module github.com/lightninglabs/subasta

require (
	github.com/btcsuite/btcd v0.20.1-beta.0.20200515232429-9f0179fd2c46
	github.com/btcsuite/btclog v0.0.0-20170628155309-84c8d2346e9f
	github.com/btcsuite/btcutil v1.0.2
	github.com/davecgh/go-spew v1.1.1
	github.com/go-errors/errors v1.0.1
	github.com/golang/protobuf v1.3.3
	github.com/jessevdk/go-flags v1.4.0
	github.com/lightninglabs/kirin v0.0.0-20200217235049-34b4e1f6a585
	github.com/lightninglabs/llm v0.0.0-20200706194325-a20d11a86b8b
	github.com/lightninglabs/loop v0.6.4-beta.0.20200617020450-0d67b3987a63
	github.com/lightninglabs/protobuf-hex-display v1.3.3-0.20191212020323-b444784ce75d
	github.com/lightningnetwork/lnd v0.10.0-beta.rc6.0.20200702223723-2d4facf94061
	github.com/lightningnetwork/lnd/cert v1.0.2
	github.com/lightningnetwork/lnd/ticker v1.0.0
	github.com/stretchr/testify v1.5.1
	github.com/urfave/cli v1.20.0
	go.etcd.io/etcd v3.3.22+incompatible
	golang.org/x/crypto v0.0.0-20200510223506-06a226fb4e37
	google.golang.org/genproto v0.0.0-20200513103714-09dca8ec2884
	google.golang.org/grpc v1.29.1
)

go 1.13

// Fix incompatibility of etcd go.mod package.
// See https://github.com/etcd-io/etcd/issues/11154
replace go.etcd.io/etcd => go.etcd.io/etcd v0.0.0-20200520232829-54ba9589114f
