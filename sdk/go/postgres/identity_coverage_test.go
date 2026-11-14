package postgres

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

func TestIdentityCoverage(t *testing.T) {
	raw, err := os.ReadFile("../../../contracts/fixtures/identity-coverage.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name, Error string
		NowMs       int64
		Policy      IdentityCoveragePolicy
		Expected    struct {
			RetainedFromMicros  int64 `json:"retainedFromMicros,string"`
			VisibleBeforeMicros int64 `json:"visibleBeforeMicros,string"`
			ValidUntilMs        int64
		}
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			got, err := DeriveIdentityEventCoverage(c.Policy, time.UnixMilli(c.NowMs))
			if c.Error != "" {
				if err == nil || err.Error() != c.Error {
					t.Fatalf("expected %s, got %v", c.Error, err)
				}
				return
			}
			want := IdentityEventCoverage(c.Expected)
			if err != nil || got != want {
				t.Fatalf("got %v, %v; want %v", got, err, want)
			}
		})
	}
}
