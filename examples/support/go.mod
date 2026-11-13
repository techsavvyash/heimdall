module github.com/techsavvyash/heimdall/examples/support

go 1.24.0

require (
	github.com/lib/pq v1.12.3
	github.com/techsavvyash/heimdall/sdk/go v0.0.0
)

require github.com/golang-jwt/jwt/v5 v5.3.1 // indirect

replace github.com/techsavvyash/heimdall/sdk/go => ../../sdk/go
