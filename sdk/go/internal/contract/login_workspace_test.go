package contract

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestLoginClaimContract(t *testing.T) {
	var cases []struct {
		Name, Error string
		Record      json.RawMessage
		Scope       LoginScope
		Now         int64
	}
	readAuditFixture(t, "login", &cases)
	schema := payloadSchema(t, "login-payload")
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			checkShape(t, schema, c.Record)
			var r LoginRecord
			if err := json.Unmarshal(c.Record, &r); err != nil {
				t.Fatal(err)
			}
			err := ValidateLoginClaim(r, c.Scope, c.Now)
			if c.Error == "" && err != nil {
				t.Fatal(err)
			}
			if c.Error != "" && !errors.Is(err, NewError(Code(c.Error), "")) {
				t.Fatalf("expected %s, got %v", c.Error, err)
			}
		})
	}
}
func TestWorkspaceGateContract(t *testing.T) {
	var cases []struct {
		Name, Error, Expected string
		Record                json.RawMessage
		Scope                 WorkspaceScope
	}
	readAuditFixture(t, "workspace-gate", &cases)
	schema := payloadSchema(t, "workspace-record")
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			var r *WorkspaceRecord
			if string(c.Record) != "null" {
				checkShape(t, schema, c.Record)
			}
			if err := json.Unmarshal(c.Record, &r); err != nil {
				t.Fatal(err)
			}
			got, err := WorkspaceGate(r, c.Scope)
			if c.Error == "" && (err != nil || got != c.Expected) {
				t.Fatalf("expected %s, got %s %v", c.Expected, got, err)
			}
			if c.Error != "" && !errors.Is(err, NewError(Code(c.Error), "")) {
				t.Fatalf("expected %s, got %v", c.Error, err)
			}
		})
	}
}
