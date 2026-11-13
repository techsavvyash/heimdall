#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
unformatted=$(gofmt -l sdk/go examples/support)
if [ -n "$unformatted" ]; then
  printf 'Run gofmt on these files:\n%s\n' "$unformatted"
  exit 1
fi
(cd sdk/go && go vet ./...)
(cd examples/support && go vet ./...)
