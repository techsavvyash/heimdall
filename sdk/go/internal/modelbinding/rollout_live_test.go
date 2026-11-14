package modelbinding

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"testing"
	"time"

	"github.com/techsavvyash/heimdall/sdk/go/internal/configuration"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
)

func TestLiveModelRollout(t *testing.T) {
	raw := os.Getenv("HEIMDALL_TEST_MODEL_ROLLOUT")
	if raw == "" {
		t.Skip("live rollout fixture absent")
	}
	var cases []struct {
		Config, Manifest             json.RawMessage
		User, Workspace, Permission  string
		Allowed, Reject, UnknownRole bool
		Membership                   []string
	}
	if err := json.Unmarshal([]byte(raw), &cases); err != nil || len(cases) == 0 {
		t.Fatal("invalid rollout fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for index, c := range cases {
		deployment, err := configuration.Parse(c.Config, c.Manifest)
		if err != nil {
			t.Fatalf("case %d: configuration: %v", index, err)
		}
		cfg := deployment.Config()
		binding := contract.Binding{ProductID: cfg.ProductID, ManifestVersion: cfg.ManifestVersion, ClientKey: cfg.ClientKey, TransportProfile: cfg.TransportProfile}
		p, err := New(Config{cfg.OpenFGA.URL, cfg.OpenFGA.StoreID, cfg.OpenFGA.ModelID, "production"}, deployment.Manifest(), binding, deployment.Artifact(), func(context.Context) (string, error) {
			return os.Getenv("HEIMDALL_PROBE_OPENFGA_KEY"), nil
		})
		if err != nil {
			t.Fatal(err)
		}
		_, err = p.Verify(ctx)
		if c.Reject {
			if !errors.Is(err, contract.ErrUnavailable) {
				t.Fatalf("case %d: expected model mismatch, got %v", index, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("case %d: verify model: %v", index, err)
		}
		allowed, err := p.Check(ctx, c.User, c.Workspace, c.Permission)
		if err != nil || allowed != c.Allowed {
			t.Fatalf("case %d: check = %v, %v", index, allowed, err)
		}
		if c.UnknownRole {
			if _, err := p.directMembership(ctx, c.User, c.Workspace); !errors.Is(err, contract.ErrUnavailable) {
				t.Fatalf("case %d: old membership reader ignored new relation: %v", index, err)
			}
		}
		if c.Membership != nil {
			expected := make(map[string]bool, len(c.Membership))
			for _, relation := range c.Membership {
				expected[relation] = true
			}
			membership, err := p.directMembership(ctx, c.User, c.Workspace)
			if err != nil || !maps.Equal(membership, expected) {
				t.Fatalf("case %d: restored membership = %v, %v", index, membership, err)
			}
		}
	}
}
