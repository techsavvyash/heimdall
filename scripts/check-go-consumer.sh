#!/bin/sh
set -eu

revision=${1:?Pass a pushed SDK tag or commit}
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
cd "$work"
export GOWORK=off
module=github.com/techsavvyash/heimdall/sdk/go
go mod init example.test/heimdall-consumer
go get "$module@$revision"
cat > main.go <<'GO'
package main

import (
	"context"
	"errors"

	heimdall "github.com/techsavvyash/heimdall/sdk/go"
	"github.com/techsavvyash/heimdall/sdk/go/postgres"
	"github.com/techsavvyash/heimdall/sdk/go/provisioning"
)

func main() {
	_, err := heimdall.New(context.Background(), []byte("{}"), []byte("{}"), heimdall.Options{})
	if !errors.Is(err, heimdall.ErrInvalidArgument) {
		panic("invalid configuration accepted")
	}
	_ = postgres.NewManagedStorage
	_ = provisioning.PrepareEnterpriseProvider
}
GO
go mod tidy
go run .
go list -m "$module"
