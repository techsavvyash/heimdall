package provisioning

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"sync/atomic"
	"testing"
)

func TestLiveIdentityEventReader(t *testing.T) {
	raw := os.Getenv("HEIMDALL_TEST_IDENTITY_EVENT_READER")
	if raw == "" {
		t.Skip("live identity event reader configuration absent")
	}
	var input struct {
		Options                              IdentityEventReaderOptions
		Since, Until, EmptySince, EmptyUntil string
		ExpectedKeys                         []string
	}
	if json.Unmarshal([]byte(raw), &input) != nil {
		t.Fatal("invalid live event test input")
	}
	reader, err := NewIdentityEventReader(input.Options)
	if err != nil {
		t.Fatal("invalid live event reader configuration")
	}
	result, err := reader.ReadWindow(context.Background(), input.Since, input.Until)
	if err != nil {
		t.Fatal("live event scan failed", err)
	}
	var keys []string
	for _, event := range result.Events {
		key, _ := json.Marshal([]string{event.AggregateType, event.AggregateID, event.Sequence})
		keys = append(keys, string(key))
	}
	slices.Sort(keys)
	if result.Requests <= 1 || !slices.Equal(keys, input.ExpectedKeys) {
		t.Fatal("live event mismatch")
	}
	empty, err := reader.ReadWindow(context.Background(), input.EmptySince, input.EmptyUntil)
	if err != nil || len(empty.Events) != 0 || empty.Requests != 1 {
		t.Fatal("live empty scan failed", err)
	}
}

func TestIdentityEventWindow(t *testing.T) {
	data, err := os.ReadFile("../../../contracts/fixtures/identity-event-window.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Since, Until string
		Cases        []struct {
			Name, Error       string
			Events            [][4]any
			ExpectedSequences []string
			Requests          int32
		}
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, c := range fixture.Cases {
		t.Run(c.Name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				var body struct {
					ResourceOwner string
					Asc           bool
					Limit         int
					Range         struct{ Since, Until string }
				}
				if r.URL.Path != "/admin/v1/events/_search" || r.Method != "POST" ||
					json.NewDecoder(r.Body).Decode(&body) != nil ||
					body.ResourceOwner != "org" || !body.Asc || body.Limit != 3 {
					t.Error("unexpected event request")
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				events := make([]map[string]any, 0, body.Limit)
				for _, row := range c.Events {
					timestamp := fmt.Sprintf("2026-11-14T00:00:00.%06.0fZ", row[0])
					if timestamp <= body.Range.Since || timestamp >= body.Range.Until {
						continue
					}
					events = append(events, map[string]any{
						"creationDate": timestamp,
						"aggregate":    map[string]any{"id": row[1], "resourceOwner": row[3], "type": map[string]string{"type": "user"}},
						"type":         map[string]string{"type": "user.added"},
						"sequence":     row[2],
						"payload":      map[string]bool{"neverExport": true},
					})
					if len(events) == body.Limit {
						break
					}
				}
				response := make(map[string]any)
				if len(c.Events) > 0 {
					response["events"] = events
				}
				_ = json.NewEncoder(w).Encode(response)
			}))
			defer server.Close()
			reader, err := NewIdentityEventReader(IdentityEventReaderOptions{
				Issuer: server.URL, Token: "token", OrganizationID: "org", TransportProfile: "loopback", PageSize: 3,
			})
			if err != nil {
				t.Fatal(err)
			}
			result, err := reader.ReadWindow(context.Background(), fixture.Since, fixture.Until)
			if c.Error != "" {
				if err == nil || err.Error() != c.Error || result.Events != nil || result.Requests != 0 {
					t.Fatal("unexpected scan error", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				var sequences []string
				for _, event := range result.Events {
					sequences = append(sequences, event.Sequence)
				}
				if !slices.Equal(sequences, c.ExpectedSequences) || result.Requests != int(c.Requests) {
					t.Fatal("unexpected event window", result)
				}
			}
			if requests.Load() != c.Requests {
				t.Fatal("unexpected request count", requests.Load())
			}
		})
	}
}
