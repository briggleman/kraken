#!/usr/bin/env bash
# Regenerate Go bindings from the .proto contracts.
# Requires: protoc on PATH (or PROTOC env var), plus protoc-gen-go and
# protoc-gen-go-grpc in $(go env GOPATH)/bin (also on PATH).
set -euo pipefail
cd "$(dirname "$0")/.."

PROTOC="${PROTOC:-protoc}"
"$PROTOC" --proto_path=proto \
  --go_out=. --go_opt=module=github.com/briggleman/kraken \
  --go-grpc_out=. --go-grpc_opt=module=github.com/briggleman/kraken \
  proto/cthulhu/agent/v1/agent.proto

echo "generated: internal/shared/agentpb/"
