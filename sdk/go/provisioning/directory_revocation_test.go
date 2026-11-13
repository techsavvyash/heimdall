package provisioning

import (
	"encoding/json"
	"errors"
	"os"
	"slices"
	"testing"

	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
)

func TestDirectoryRevocationFixtures(t *testing.T) {
	raw, err := os.ReadFile("../../../contracts/fixtures/directory-revocation-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		DirectoryRevocationInput
		Name, Error, Action string
		Roles               []string
	}
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	if len(fixtures) == 0 {
		t.Fatal("missing fixture cases")
	}
	for _, fixture := range fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			input := fixture.DirectoryRevocationInput
			input.RuleID, input.OwnerRole = "employees", "owner"
			result, err := PlanDirectoryRevocation(input)
			if fixture.Error != "" {
				if !errors.Is(err, contract.NewError(contract.Code(fixture.Error), "")) {
					t.Fatal(err)
				}
				return
			}
			if err != nil || result.Action != fixture.Action || !slices.Equal(result.Roles, fixture.Roles) {
				t.Fatalf("got %+v, %v", result, err)
			}
		})
	}
}
