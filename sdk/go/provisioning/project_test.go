package provisioning

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"github.com/techsavvyash/heimdall/sdk/go/internal/postgres"
)

func TestProvisionProjectLive(t *testing.T) {
	dsn, token := os.Getenv("HEIMDALL_TEST_SETUP_DSN"), os.Getenv("HEIMDALL_PROBE_ZITADEL_PAT")
	if dsn == "" || token == "" {
		t.Skip("requires disposable database and ZITADEL verification credentials")
	}
	connector, err := postgres.NewConnector(dsn)
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(connector)
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	o := projectOptions{
		scope:  postgres.SetupScope{ProductID: "support", Environment: "provider-go", Fingerprint: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", StepCount: 3},
		issuer: os.Getenv("HEIMDALL_PROBE_ZITADEL_ISSUER"), organizationID: os.Getenv("HEIMDALL_TEST_SETUP_ZITADEL_ORG"),
		name: "Support verification", token: token,
	}
	project, err := provisionProject(ctx, db, o)
	if err != nil || project == "" {
		t.Fatal("create failed", err)
	}
	again, err := provisionProject(ctx, db, o)
	if err != nil || again != project {
		t.Fatal("repeat did not return the project", err)
	}
	o.name = "Wrong name"
	if _, err := provisionProject(ctx, db, o); !errors.Is(err, contract.ErrConflict) {
		t.Fatal("drift accepted", err)
	}
}
