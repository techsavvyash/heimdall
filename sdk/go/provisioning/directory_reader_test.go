package provisioning

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
)

func TestDirectoryReaderContract(t *testing.T) {
	raw, err := os.ReadFile("../../../contracts/fixtures/directory-reader-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures struct {
		Cases []struct {
			Name, User, Owner, Expected, ExternalID string
			SCIM                                    json.RawMessage
			ListStatus                              int
		}
	}
	if err := json.Unmarshal(raw, &fixtures); err != nil || len(fixtures.Cases) == 0 {
		t.Fatal("missing contract cases", err)
	}
	for _, f := range fixtures.Cases {
		t.Run(f.Name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer viewer" {
					t.Error("unexpected request")
				}
				switch r.URL.RequestURI() {
				case "/v2/users/employee":
					if f.User == "" {
						w.WriteHeader(404)
						return
					}
					owner := f.Owner
					if owner == "" {
						owner = "org-a"
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"user": map[string]any{
						"userId": "employee", "state": f.User, "human": map[string]any{},
						"details": map[string]string{"resourceOwner": owner},
					}})
				case "/scim/v2/org-a/Users?startIndex=1&count=1":
					if f.ListStatus != 0 {
						w.WriteHeader(f.ListStatus)
					}
					_, _ = w.Write([]byte(`{"startIndex":1,"totalResults":0,"Resources":[]}`))
				case "/scim/v2/org-a/Users/employee":
					if f.SCIM == nil {
						w.WriteHeader(404)
						return
					}
					_, _ = w.Write(f.SCIM)
				default:
					t.Error("unexpected endpoint")
					w.WriteHeader(500)
				}
			}))
			defer server.Close()
			reader, err := NewDirectoryReader(DirectoryReaderOptions{server.URL, "org-a", "viewer", "loopback"})
			if err != nil {
				t.Fatal(err)
			}
			state, err := reader.Read(context.Background(), "employee")
			switch f.Expected {
			case "conflict", "unavailable":
				if !errors.Is(err, contract.NewError(contract.Code(f.Expected), "")) {
					t.Fatal("wrong error", err)
				}
			default:
				if err != nil || state != (DirectoryIdentityState{f.Expected, f.ExternalID}) {
					t.Fatal("wrong state", state, err)
				}
			}
		})
	}
}

func TestLiveDirectoryReader(t *testing.T) {
	raw := os.Getenv("HEIMDALL_TEST_DIRECTORY_READER")
	if raw == "" {
		t.Skip("pinned ZITADEL integration")
	}
	var input struct {
		DirectoryReaderOptions
		Subject, State, ExternalID string
	}
	if json.Unmarshal([]byte(raw), &input) != nil {
		t.Fatal("invalid test input")
	}
	reader, err := NewDirectoryReader(input.DirectoryReaderOptions)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	state, err := reader.Read(ctx, input.Subject)
	if err != nil || state != (DirectoryIdentityState{input.State, input.ExternalID}) {
		t.Fatal("unexpected live identity result", err)
	}
	page, err := reader.List(ctx, 1, 1)
	if err != nil {
		t.Fatal("live listing failed", err)
	}
	if input.State == "active" && (len(page.Users) != 1 || page.Users[0].ID != input.Subject ||
		page.Users[0].ExternalID != input.ExternalID || page.NextStartIndex != 2) {
		t.Fatal("live listing did not identify the scoped user")
	}
	if input.State == "missing" && (len(page.Users) != 0 || page.NextStartIndex != 0) {
		t.Fatal("live listing did not terminate")
	}
}
