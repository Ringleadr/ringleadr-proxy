#!/usr/bin/env bash

set -e

TAG=$(git rev-parse --short HEAD)

env GOOS=linux GOARCH=amd64 go build -o build/agogos-proxy main.go
docker build -t edwarddobson/agogos-proxy:$TAG .