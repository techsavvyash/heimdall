package provisioning

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	_ "github.com/lib/pq"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
)

func TestDiscoveryRebuildProbe(t *testing.T) {
	raw := os.Getenv("HEIMDALL_DISCOVERY_REBUILD_INPUT")
	if raw == "" {
		t.Skip("reference product probe")
	}
	var input struct {
		Config, Manifest json.RawMessage
		Options          DiscoveryRebuildOptions
	}
	if err := json.Unmarshal([]byte(raw), &input); err != nil {
		t.Fatal("invalid rebuild probe input")
	}
	input.Options.OpenFGAToken = os.Getenv("HEIMDALL_PROBE_OPENFGA_KEY")
	db, err := sql.Open("postgres", os.Getenv("HEIMDALL_DISCOVERY_REBUILD_DATABASE"))
	if err != nil {
		t.Fatal("invalid rebuild probe database")
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := RebuildDiscovery(ctx, db, input.Config, input.Manifest, input.Options)
	if err != nil {
		if known, ok := err.(*contract.Error); ok {
			fmt.Println("REBUILD_PROBE_ERROR", known.Code)
			return
		}
		t.Fatal("rebuild probe failed")
	}
	body, err := json.Marshal(result)
	if err != nil {
		t.Fatal("invalid rebuild probe result")
	}
	fmt.Println("REBUILD_PROBE", string(body))
}
