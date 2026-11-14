package provisioning

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"
)

func TestLiveModelRolloutInspection(t *testing.T) {
	raw := os.Getenv("HEIMDALL_TEST_MODEL_INSPECTION")
	if raw == "" {
		t.Skip("live rollout inspection fixture absent")
	}
	type deployment struct {
		Config, Manifest json.RawMessage
	}
	var cases []struct {
		Name               string
		Current, Candidate deployment
		Report             ModelRolloutReport
		Error              string
	}
	if err := json.Unmarshal([]byte(raw), &cases); err != nil || len(cases) == 0 {
		t.Fatal("invalid inspection fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			report, err := InspectModelRollout(ctx,
				ModelRolloutDeployment{c.Current.Config, c.Current.Manifest},
				ModelRolloutDeployment{c.Candidate.Config, c.Candidate.Manifest},
				os.Getenv("HEIMDALL_PROBE_OPENFGA_KEY"))
			if c.Error != "" {
				if err == nil || err.Error() != c.Error {
					t.Fatalf("wanted %s, got %v", c.Error, err)
				}
			} else if err != nil || !reflect.DeepEqual(report, c.Report) {
				t.Fatalf("expected %+v, got %+v (%v)", c.Report, report, err)
			}
		})
	}
}
