package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"testing"

	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
)

func TestRetirementScan(t *testing.T) {
	url := os.Getenv("HEIMDALL_TEST_RETIRE_SCAN_URL")
	if url == "" {
		t.Skip("retained scan database required")
	}
	connector, err := NewConnector(url)
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(connector)
	defer db.Close()
	var fixture struct {
		Cases []struct {
			Name, ProductID, Environment string
			TenantID                     *string
			Now                          int64
			AfterRetirement              bool
			EventIDs, Scanned, Excluded  []string
		}
	}
	raw, err := os.ReadFile("../../../../contracts/fixtures/retirement-scan.json")
	if err != nil || json.Unmarshal(raw, &fixture) != nil {
		t.Fatal("invalid retirement scan fixture", err)
	}
	ctx := context.Background()
	for _, c := range fixture.Cases {
		if c.AfterRetirement != (os.Getenv("HEIMDALL_TEST_RETIRE_SCAN_AFTER") == "1") {
			continue
		}
		t.Run(c.Name, func(t *testing.T) {
			port, err := NewRetainedRuntimeAudit(db, c.ProductID, c.Environment, []string{"siem"}, backfillPort(t, db, c.ProductID).validate)
			if err != nil {
				t.Fatal(err)
			}
			now := c.Now
			if now == 0 {
				now = 10000
			}
			const duration = 1000
			var after *RetirementCursor
			var scanned, found []string
			for range 100 {
				page, err := port.ScanRetirementCandidates(ctx, c.TenantID, now, duration, after, 1)
				if err != nil {
					t.Fatal(err)
				}
				found = append(found, page.EventIDs...)
				if page.Next == nil {
					break
				}
				scanned = append(scanned, page.Next.EventID)
				if _, err := port.ScanRetirementCandidates(ctx, c.TenantID, now+1, duration, page.Next, 1); !errors.Is(err, contract.ErrInvalidArgument) {
					t.Fatal("changed sweep time", err)
				}
				other := "different-tenant"
				if _, err := port.ScanRetirementCandidates(ctx, &other, now, duration, page.Next, 1); !errors.Is(err, contract.ErrInvalidArgument) {
					t.Fatal("changed scope", err)
				}
				after = page.Next
			}
			if len(scanned) == 100 || (c.Scanned != nil && !slices.Equal(scanned, c.Scanned)) ||
				(c.EventIDs != nil && !slices.Equal(found, c.EventIDs)) {
				t.Fatal("unexpected scan", scanned, found)
			}
			for _, id := range c.Excluded {
				if (c.AfterRetirement && slices.Contains(scanned, id)) || slices.Contains(found, id) {
					t.Fatal("unexpected retired or protected root", id)
				}
			}
			cancelled, cancel := context.WithCancel(ctx)
			cancel()
			if _, err := port.ScanRetirementCandidates(cancelled, c.TenantID, now, duration, nil, 1); !errors.Is(err, contract.ErrCancelled) {
				t.Fatal("cancelled scan", err)
			}
		})
	}
}
