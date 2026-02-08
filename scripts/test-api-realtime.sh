#!/usr/bin/env bash

set -euo pipefail

cd api_realtime
go test ./...
