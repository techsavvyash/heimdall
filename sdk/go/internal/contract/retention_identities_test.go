package contract

import (
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"testing"
)

func TestRetentionIdentityValidation(t *testing.T) {
	var f struct {
		Scope  RetentionScope
		Events map[string]map[string]any
		Cases  []struct {
			Name, Resolution string
			Bases            []string
			Valid            bool
			ScopePatch       map[string]string
			BadDigest        bool
			Routing          json.RawMessage
			DeliveryRows     *int
			PatchIndex       *int
			EventPatch       map[string]any
		}
	}
	raw, err := os.ReadFile("../../../../contracts/fixtures/retention-identity-validation.json")
	if err != nil {
		t.Fatal(err)
	}
	if json.Unmarshal(raw, &f) != nil {
		t.Fatal("fixture")
	}
	for _, c := range f.Cases {
		t.Run(c.Name, func(t *testing.T) {
			scope := f.Scope
			if p, ok := c.ScopePatch["productId"]; ok {
				scope.ProductID = p
			}
			if tenant, ok := c.ScopePatch["tenantId"]; ok {
				scope.TenantID = &tenant
			}
			sources := []RetentionSource{}
			for index, base := range c.Bases {
				data, _ := json.Marshal(f.Events[base])
				var event map[string]any
				_ = json.Unmarshal(data, &event)
				if c.PatchIndex != nil && index == *c.PatchIndex {
					for k, v := range c.EventPatch {
						event[k] = v
					}
				}
				digest, e := AuditDigest(event)
				if e != nil {
					t.Fatal(e)
				}
				if c.BadDigest {
					digest = "bad"
				}
				routing := []string{"z-sink", "a-sink"}
				if c.Routing != nil {
					if json.Unmarshal(c.Routing, &routing) != nil {
						t.Fatal("routing")
					}
				}
				rows := 2
				if c.DeliveryRows != nil {
					rows = *c.DeliveryRows
				}
				sources = append(sources, RetentionSource{event, digest, routing, rows})
			}
			before, _ := json.Marshal(sources)
			result, e := BuildRetentionIdentities(scope, c.Resolution, sources)
			if !c.Valid {
				if e != ErrAuditUnavailable {
					t.Fatalf("wanted audit_unavailable: %v", e)
				}
			} else {
				if e != nil {
					t.Fatal(e)
				}
				if len(result) != len(sources) {
					t.Fatal("count")
				}
				if !sort.SliceIsSorted(result, func(i, j int) bool { return result[i].EventID < result[j].EventID }) {
					t.Fatal("order")
				}
				for _, identity := range result {
					for _, s := range sources {
						if s.Event["eventId"] != identity.EventID {
							continue
						}
						kind := "ordinary"
						switch s.Event["eventType"] {
						case "heimdall.authorization.requested":
							kind = "request"
						case "heimdall.authorization.completed":
							kind = "terminal"
						case "heimdall.recovery.completed":
							kind = "recovery"
						}
						if identity.Kind != kind || identity.Digest != s.Digest || identity.RecordedAt != int64(s.Event["recordedAt"].(float64)) || !reflect.DeepEqual(identity.RetentionScope, scope) || !reflect.DeepEqual(identity.Routing, []string{"a-sink", "z-sink"}) {
							t.Fatal("identity")
						}
						if kind == "ordinary" {
							if identity.DecisionID != nil {
								t.Fatal("ordinary binding")
							}
						} else if identity.DecisionID == nil || *identity.DecisionID != s.Event["decisionId"] {
							t.Fatal("decision")
						}
					}
				}
				result[0].Routing[0] = "changed"
				*result[0].TenantID = "changed"
			}
			after, _ := json.Marshal(sources)
			if string(before) != string(after) {
				t.Fatal("source mutation")
			}
		})
	}
}
