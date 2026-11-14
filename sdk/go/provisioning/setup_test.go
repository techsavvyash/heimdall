package provisioning

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/techsavvyash/heimdall/sdk/go/internal/configuration"
	"github.com/techsavvyash/heimdall/sdk/go/internal/postgres"
)

func mustSetupPlanJSON(t *testing.T, plan ProductSetupPlan) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]string{"productId": plan.ProductID, "environment": plan.Environment,
		"issuer": plan.Issuer, "organizationId": plan.OrganizationID, "projectName": plan.ProjectName, "openfgaUrl": plan.OpenFGAURL})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestSetupProductCrossLanguage(t *testing.T) {
	dsn, org := os.Getenv("HEIMDALL_TEST_SETUP_DSN"), os.Getenv("HEIMDALL_TEST_SETUP_ZITADEL_ORG")
	if dsn == "" || org == "" {
		t.Skip("requires a disposable setup journal and ZITADEL organization")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 55*time.Second)
	defer cancel()
	issuer, base := os.Getenv("HEIMDALL_PROBE_ZITADEL_ISSUER"), os.Getenv("HEIMDALL_PROBE_OPENFGA_URL")
	zToken, fToken := os.Getenv("HEIMDALL_PROBE_ZITADEL_PAT"), os.Getenv("HEIMDALL_PROBE_OPENFGA_KEY")
	tmp := t.TempDir()
	secretDir := filepath.Join(tmp, "secrets")
	if err := os.Mkdir(secretDir, 0700); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(tmp, "manifest.json")
	planPath := filepath.Join(tmp, "plan.json")
	fixture, err := os.ReadFile("../../../contracts/fixtures/configuration-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var input struct{ Manifest json.RawMessage }
	if json.Unmarshal(fixture, &input) != nil {
		t.Fatal("fixture")
	}
	if err := os.WriteFile(manifestPath, input.Manifest, 0600); err != nil {
		t.Fatal(err)
	}
	plan := ProductSetupPlan{ProductID: "ledger", Environment: "setup-cross-language", Issuer: issuer, OrganizationID: org,
		ProjectName: "Ledger <setup>\u2028verification", OpenFGAURL: base}
	raw, err := json.Marshal(map[string]string{"productId": plan.ProductID, "environment": plan.Environment,
		"issuer": issuer, "organizationId": org, "projectName": plan.ProjectName, "openfgaUrl": base})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(planPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(ctx, "node", "../../typescript/dist/cli.js", planPath, manifestPath)
	command.Env = append(os.Environ(), "HEIMDALL_SETUP_DATABASE_URL="+dsn, "HEIMDALL_SETUP_ZITADEL_TOKEN="+zToken,
		"HEIMDALL_SETUP_OPENFGA_TOKEN="+fToken, "HEIMDALL_SETUP_SECRET_DIR="+secretDir)
	output, err := command.Output()
	if err != nil {
		t.Fatal("TypeScript CLI failed", err)
	}
	status := exec.CommandContext(ctx, "node", "../../typescript/dist/cli.js", "--status", plan.ProductID, plan.Environment)
	status.Env = []string{"HEIMDALL_SETUP_DATABASE_URL=" + dsn, "PATH=" + os.Getenv("PATH")}
	statusJSON, err := status.Output()
	if err != nil {
		t.Fatal("status without provider credentials", err)
	}
	var progress struct {
		StepCount int
		Steps     []struct{ State string }
	}
	if json.Unmarshal(statusJSON, &progress) != nil || progress.StepCount != 4 || len(progress.Steps) != 4 {
		t.Fatal("invalid setup progress")
	}
	for _, step := range progress.Steps {
		if step.State != "confirmed" {
			t.Fatal("unconfirmed setup step")
		}
	}
	var configs map[string]json.RawMessage
	if json.Unmarshal(output, &configs) != nil {
		t.Fatal("CLI returned invalid JSON")
	}
	ts, err := configuration.Parse(configs["web"], input.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	config := ts.Config()
	t.Cleanup(func() { deleteVerificationStore(t, base, fToken, config.OpenFGA.StoreID) })
	connector, err := postgres.NewConnector(dsn)
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(connector)
	defer db.Close()
	goStatus, err := ReadSetupStatus(ctx, db, plan.ProductID, plan.Environment)
	if err != nil {
		t.Fatal(err)
	}
	var tsStatus SetupStatus
	if json.Unmarshal(statusJSON, &tsStatus) != nil || !reflect.DeepEqual(goStatus, tsStatus) {
		t.Fatal("status differs across languages")
	}
	options := ProductSetupOptions{ZitadelToken: zToken, OpenFGAToken: fToken,
		SaveSecret: func(context.Context, string, string, string) error {
			t.Error("resume attempted to save a new secret")
			return nil
		},
		HasSecret: func(_ context.Context, id, client string) (bool, error) {
			info, err := os.Stat(filepath.Join(secretDir, id+"."+client+".secret"))
			return err == nil && info.Size() > 0 && info.Mode().Perm() == 0600, err
		}}
	result, err := SetupProduct(ctx, db, plan, input.Manifest, options)
	if err != nil {
		t.Fatal("Go did not resume the TypeScript plan", err)
	}
	goConfig, err := configuration.Parse(result["web"], input.Manifest)
	if err != nil || !reflect.DeepEqual(goConfig.Config(), config) {
		t.Fatal("configuration differs", err)
	}
	// A second product shares the provider deployment and journal, with independent pins.
	modelFixture, err := os.ReadFile("../../../contracts/fixtures/tenant-model-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var models []struct {
		Name     string
		Manifest json.RawMessage
	}
	if json.Unmarshal(modelFixture, &models) != nil {
		t.Fatal("invalid product manifests")
	}
	var supportManifest json.RawMessage
	for _, model := range models {
		if model.Name == "support" {
			supportManifest = model.Manifest
		}
	}
	supportPlan := plan
	supportPlan.ProductID, supportPlan.ProjectName = "support", "Support setup verification"
	supportOptions := options
	var secretSaved bool
	supportOptions.SaveSecret = func(_ context.Context, id, client, secret string) error {
		secretSaved = secret != ""
		return os.WriteFile(filepath.Join(secretDir, id+"."+client+".secret"), []byte(secret), 0600)
	}
	supportOptions.HasSecret = func(context.Context, string, string) (bool, error) { return secretSaved, nil }
	support, err := SetupProduct(ctx, db, supportPlan, supportManifest, supportOptions)
	if err != nil {
		t.Fatal("Support setup failed", err)
	}
	supportConfig, err := configuration.Parse(support["web"], supportManifest)
	if err != nil {
		t.Fatal(err)
	}
	sc := supportConfig.Config()
	t.Cleanup(func() { deleteVerificationStore(t, base, fToken, sc.OpenFGA.StoreID) })
	if !secretSaved || sc.Audience == config.Audience || sc.ClientID == config.ClientID ||
		sc.OpenFGA.StoreID == config.OpenFGA.StoreID || sc.OpenFGA.ModelID == config.OpenFGA.ModelID {
		t.Fatal("products must have independent configuration")
	}
	if err := os.WriteFile(planPath, mustSetupPlanJSON(t, supportPlan), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, supportManifest, 0600); err != nil {
		t.Fatal(err)
	}
	resume := exec.CommandContext(ctx, "node", "../../typescript/dist/cli.js", planPath, manifestPath)
	resume.Env = command.Env
	tsSupport, err := resume.Output()
	if err != nil {
		t.Fatal("TypeScript CLI did not resume the Go plan", err)
	}
	var supportConfigs map[string]json.RawMessage
	if json.Unmarshal(tsSupport, &supportConfigs) != nil {
		t.Fatal("invalid Support configuration")
	}
	resumed, err := configuration.Parse(supportConfigs["web"], supportManifest)
	if err != nil || !reflect.DeepEqual(resumed.Config(), sc) {
		t.Fatal("Support configuration differs across languages", err)
	}
}
