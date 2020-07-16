#!/bin/bash

set -e

# Find out what version of llm to use for the imported auctioneer.proto by
# parsing the go.mod file.
LAST_LINE=$(grep "lightninglabs/llm" ../go.mod | tail -n 1)
PKG_PATH=""
if [[ $LAST_LINE =~ "replace" ]]
then
  # There is a replace directive. Use awk to split by spaces then extract field
  # 4 and 5 and stitch them together with an @ which will resolve into
  # github.com/lightninglabs/llm@v0.x.x-yyyymmddhhmiss-shortcommit.
  PKG_PATH=$(echo $LAST_LINE | awk -F " " '{ print $4"@"$5 }')
else
  # This is a normal directive, just combine field 1 and 2 with an @.
  PKG_PATH=$(echo $LAST_LINE | awk -F " " '{ print $1"@"$2 }')
fi

# Generate the gRPC bindings for all proto files.
for file in ./*.proto
do
	protoc -I/usr/local/include -I. \
	       -I$GOPATH/pkg/mod/$PKG_PATH \
	       --go_out=plugins=grpc,paths=source_relative:. \
		${file}

done
