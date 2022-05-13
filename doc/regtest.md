# Local regtest auction server setup

This document is intended to be given to users who want to integrate the regtest
auction server into their own environment.

To run the auction server, you need access to the `auctionserver` docker image.
Because it is not yet clear whether this image will be available publicly or
only given to selected partners, we will reference the image in this example
by `REPOSITORY/auctionserver:v0.10.13-alpha-regtest-only`.

## Prerequisites

- `docker` must be installed
- an `lnd` node for the auction server must be running and unlocked. We assume
  for this example that a docker container named `lnd-auctionserver` is running,
  has the RPC port `10009` exposed and a data volume mounted to
  `/data/lnd-auctionserver` where that node is storing its data (e.g.
  `--lnddir=/data/lnd-auctionserver`).
- any Pool trader clients that are to interact with the auction server MUST each
  have their own `lnd` node.
- Pool trader clients that submit ask orders MUST have an announced external IP
  under which they can be reached from nodes that submit bid orders (e.g.
  `--externalip=<docker-container-name-of-node>`)
- Pool trader clients must start their trader daemons with the `--fakeauth` flag
  in order to work without `aperture`.
- Pool trader clients must have access to the self-signed TLS certificate of the
  auction server.

## Auction server setup

To start the auction server docker container, run the following commands (after
replacing the placeholders and adjusting the example paths to your environment):

```shell
$ docker pull REPOSITORY/auctionserver:v0.10.13-alpha-regtest-only
$ docker run -d \
  --name "auctionserver" \
  --hostname "auctionserver" \
  --restart unless-stopped \
  -p 12009:12009 \
  -p 12080:12080 \
  -v /data/lnd-auctionserver:/root/.lnd \
  -v /data/auctionserver:/root/.auctionserver \
  auctionserver:v0.10.13-alpha-regtest-only \
    daemon \
    --tlsextradomain="auctionserver" \
    --lnd.host=lnd-auctionserver:10009 \
    --lnd.macaroondir=/root/.lnd/data/chain/bitcoin/regtest \
    --lnd.tlspath=/root/.lnd/tls.cert
```

## Issue auction server admin commands

Once the auction server docker container is running, the admin CLI can be used
by running:

```shell
$ docker exec -ti auctionserver auctioncli <category> <subcommand>
```

A list of all categories can be shown by running:

```shell
$ docker exec -ti auctionserver auctioncli --help

NAME:
   auctioncli - control plane for the auctioneer server

USAGE:
   auctioncli [global options] command [command options] [arguments...]

VERSION:
   0.10.13-alpha commit=regtest

COMMANDS:
   help, h  Shows a list of commands or help for one command

   Auction:
     auction, a  Interact with the auction.

   Master Account:
     masteraccount, m  Interact with the master account.
```

To list all sub commands of a category, run:

```shell
$ docker exec -ti auctionserver auctioncli <category> --help
```

For example:

```shell
$ docker exec -ti auctionserver auctioncli auction --help

NAME:
   auctioncli auction - Interact with the auction.

USAGE:
   auctioncli auction command [command options] [arguments...]

COMMANDS:
   batchtick, t    manually force a new batch tick event
   pausebatch, p   manually pause all batch tick events
   resumebatch, r  manually resume all batch tick events
   status, s       query the current status of the auction
```

### Manually forcing a batch tick

By default, the auction server attempts to create a batch every 10 minutes. This
can be too long of a period for automated testing. To manually force a batch
tick, use the following sub command:

```shell
$ docker exec -ti auctionserver auctioncli auction batchtick
```

Observe the auction server's log to see whether a batch was created successfully
or not.

## Trader client daemon setup

To start a local trader daemon (`poold`), we assume an `lnd` node called `alice`
is running and reachable on `localhost:10010` (RPC) that stores its data in
`/data/lnd-alice`:

```shell
$ poold \
  --network=regtest \
  --fakeauth \
  --auctionserver=localhost:12009 \
  --tlspathauctserver=/data/auctionserver/tls.cert
  --lnd.host=localhost:10010 \
  --lnd.macaroonpath=/data/lnd-alice/data/chain/bitcoin/regtest/admin.macaroon \
  --lnd.tlspath=/data/lnd-alice/tls.cert
```
