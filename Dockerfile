FROM golang:1.13-alpine as builder

# Copy in the local repository to build from.
COPY . /go/src/github.com/lightninglabs/agora

# Force Go to use the cgo based DNS resolver. This is required to ensure DNS
# queries required to connect to linked containers succeed.
ENV GODEBUG netdns=cgo

# Explicitly turn on the use of modules (until this becomes the default).
ENV GO111MODULE on

# Install dependencies and install/build both auctioneer binaries (daemon and
# CLI).
RUN apk add --no-cache --update alpine-sdk \
    git \
    make \
&&  cd /go/src/github.com/lightninglabs/agora/cmd \
&&  go install -v ./...

# Start a new, final image to reduce size.
FROM alpine as final

# Expose the port needed for the admin RPC server, as well as the public gRPC
# service that runs the entire system.
EXPOSE 12009
EXPOSE 13370

# Copy over both the daemon and CLI binaries from the builder image.
COPY --from=builder /go/bin/agoraserver /bin/
COPY --from=builder /go/bin/auctioncli /bin/

# Add bash.
RUN apk add --no-cache \
    bash \
    ca-certificates

# Specify the start command and entrypoint as the subasta daemon.
ENTRYPOINT ["agoraserver", "daemon"]
