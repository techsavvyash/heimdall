package provisioning

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/techsavvyash/heimdall/sdk/go/internal/postgres"
)

func TestProvisionOpenFGALive(t *testing.T) {
	dsn, token := os.Getenv("HEIMDALL_TEST_SETUP_DSN"), os.Getenv("HEIMDALL_PROBE_OPENFGA_KEY")
	if dsn == "" || token == "" {
		t.Skip("requires disposable database and OpenFGA verification credentials")
	}
	connector, err := postgres.NewConnector(dsn)
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(connector)
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	raw, err := os.ReadFile("../../../contracts/fixtures/configuration-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Config   map[string]any
		Manifest json.RawMessage
	}
	if json.Unmarshal(raw, &fixture) != nil {
		t.Fatal("invalid fixture")
	}
	base := os.Getenv("HEIMDALL_PROBE_OPENFGA_URL")
	scope := postgres.SetupScope{ProductID: "ledger", Environment: "fga-go", StepCount: 4, Fingerprint: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	fixture.Config["environment"] = scope.Environment
	fixture.Config["openfga"].(map[string]any)["url"] = base
	config, err := json.Marshal(fixture.Config)
	if err != nil {
		t.Fatal(err)
	}
	for i, resource := range []string{"verified-project", "verified-client"} {
		id, err := newDispatchID()
		if err != nil {
			t.Fatal(err)
		}
		if _, err = postgres.ClaimSetupStep(ctx, db, scope, i+1, id); err != nil {
			t.Fatal(err)
		}
		if err = postgres.ConfirmSetupStep(ctx, db, scope, i+1, id, resource); err != nil {
			t.Fatal(err)
		}
	}
	o := openFGAOptions{scope: scope, configJSON: config, manifestJSON: fixture.Manifest, token: token}
	binding, err := provisionOpenFGA(ctx, db, o)
	if err != nil {
		t.Fatal("OpenFGA setup failed", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodDelete, base+"/stores/"+binding.StoreID, nil)
		if err != nil {
			t.Error("cleanup request")
			return
		}
		req.Header.Set("Authorization", "Bearer "+token)
		client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		res, err := client.Do(req)
		if err != nil {
			t.Error("cleanup failed")
			return
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusNoContent {
			t.Error("cleanup status", res.StatusCode)
		}
	})
	again, err := provisionOpenFGA(ctx, db, o)
	if err != nil || again != binding {
		t.Fatal("resume failed", err)
	}
}
