#!/bin/bash
MQ_INSTALLATION_PATH=${PWD}/ibmmq_dist

export CGO_CFLAGS="-I$MQ_INSTALLATION_PATH/inc"
export CGO_LDFLAGS="-L$MQ_INSTALLATION_PATH/lib64 -Wl,-rpath,$MQ_INSTALLATION_PATH/lib64"
go build -o out/linux64/mqConnector