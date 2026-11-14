package modelbinding

import (
	"context"
	"encoding/json"
	"github.com/techsavvyash/heimdall/sdk/go/internal/configuration"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestBinding(t *testing.T) {
	raw, e := os.ReadFile("../../../../contracts/fixtures/tenant-model-v1.json")
	if e != nil {
		t.Fatal(e)
	}
	var fixtures []struct {
		Manifest contract.Manifest
		Binding  contract.Binding
		Expected contract.TenantModel
	}
	if e = json.Unmarshal(raw, &fixtures); e != nil {
		t.Fatal(e)
	}
	f := fixtures[0]
	raw, e = os.ReadFile("../../../../contracts/fixtures/model-binding.json")
	if e != nil {
		t.Fatal(e)
	}
	var cases []struct {
		Name     string
		Response json.RawMessage
		Accepted bool
	}
	if e = json.Unmarshal(raw, &cases); e != nil {
		t.Fatal(e)
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != "/stores/store/authorization-models/pinned-model" || r.Header.Get("Authorization") != "Bearer secret" {
					t.Error("wrong request")
				}
				w.Write(c.Response)
			}))
			defer s.Close()
			p, e := New(Config{s.URL, "store", "pinned-model", "loopback"}, f.Manifest, f.Binding, f.Expected, func(context.Context) (string, error) { return "secret", nil })
			if e != nil {
				t.Fatal(e)
			}
			got, e := p.Verify(context.Background())
			if c.Accepted {
				if e != nil || got.ModelHash() != f.Expected.ModelHash {
					t.Fatalf("unexpected result: %v", e)
				}
			} else if e != contract.ErrUnavailable {
				t.Fatalf("accepted drift: %v", e)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if _, e = p.Verify(ctx); e != contract.ErrCancelled {
				t.Fatalf("cancellation: %v", e)
			}
		})
	}
	f.Expected.ModelHash = "wrong"
	if _, e = New(Config{"http://127.0.0.1", "store", "pinned-model", "loopback"}, f.Manifest, f.Binding, f.Expected, func(context.Context) (string, error) { t.Fatal("unexpected credential load"); return "", nil }); e != contract.ErrInvalidArgument {
		t.Fatal("accepted local mismatch")
	}
}

func TestLiveBinding(t *testing.T) {
	raw := os.Getenv("HEIMDALL_TEST_MODEL_BINDING")
	if raw == "" {
		t.Skip("live credentials absent")
	}
	var config struct{ URL, StoreID, ModelID, Name string }
	if e := json.Unmarshal([]byte(raw), &config); e != nil {
		t.Fatal(e)
	}
	data, e := os.ReadFile("../../../../contracts/fixtures/tenant-model-v1.json")
	if e != nil {
		t.Fatal(e)
	}
	var fixtures []struct {
		Name     string
		Manifest contract.Manifest
		Binding  contract.Binding
		Expected contract.TenantModel
	}
	if e = json.Unmarshal(data, &fixtures); e != nil {
		t.Fatal(e)
	}
	for _, f := range fixtures {
		if f.Name != config.Name {
			continue
		}
		data, e := os.ReadFile("../../../../contracts/fixtures/configuration-v1.json")
		if e != nil {
			t.Fatal(e)
		}
		var base struct{ Config map[string]any }
		if e = json.Unmarshal(data, &base); e != nil {
			t.Fatal(e)
		}
		base.Config["productId"] = f.Manifest.ProductID
		base.Config["manifestVersion"] = f.Binding.ManifestVersion
		base.Config["clientKey"] = f.Binding.ClientKey
		base.Config["openfga"] = map[string]any{"url": config.URL, "storeId": config.StoreID, "modelId": config.ModelID}
		configJSON, _ := json.Marshal(base.Config)
		manifestJSON, _ := json.Marshal(f.Manifest)
		deployment, e := configuration.Parse(configJSON, manifestJSON)
		if e != nil {
			t.Fatal(e)
		}
		bound := deployment.Config()
		binding := contract.Binding{ProductID: bound.ProductID, ManifestVersion: bound.ManifestVersion, ClientKey: bound.ClientKey, TransportProfile: bound.TransportProfile}
		p, e := New(Config{bound.OpenFGA.URL, bound.OpenFGA.StoreID, bound.OpenFGA.ModelID, "production"}, deployment.Manifest(), binding, deployment.Artifact(), func(context.Context) (string, error) { return os.Getenv("HEIMDALL_PROBE_OPENFGA_KEY"), nil })
		if e != nil {
			t.Fatal(e)
		}
		v, e := p.Verify(context.Background())
		if e != nil || v.ModelHash() != f.Expected.ModelHash {
			t.Fatalf("live verification failed: %v", e)
		}
		return
	}
	t.Fatal("unknown fixture")
}

func TestTransport(t *testing.T) {
	raw, e := os.ReadFile("../../../../contracts/fixtures/tenant-model-v1.json")
	if e != nil {
		t.Fatal(e)
	}
	var fixtures []struct {
		Manifest contract.Manifest
		Binding  contract.Binding
		Expected contract.TenantModel
	}
	if e = json.Unmarshal(raw, &fixtures); e != nil {
		t.Fatal(e)
	}
	f := fixtures[0]
	for _, status := range []int{301, 401, 403, 404, 500} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
		p, e := New(Config{s.URL, "store", "pinned-model", "loopback"}, f.Manifest, f.Binding, f.Expected, func(context.Context) (string, error) { return "secret", nil })
		if e != nil {
			t.Fatal(e)
		}
		if _, e = p.Verify(context.Background()); e != contract.ErrUnavailable {
			t.Errorf("status %d: %v", status, e)
		}
		s.Close()
	}
	release := make(chan struct{})
	defer close(release)
	p, e := New(Config{"http://127.0.0.1", "store", "pinned-model", "loopback"}, f.Manifest, f.Binding, f.Expected, func(context.Context) (string, error) { <-release; return "secret", nil })
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, e = p.Verify(ctx); e != contract.ErrDeadlineExceeded {
		t.Fatalf("deadline: %v", e)
	}
	f.Expected.Permissions["invoice.read"] = "member"
	if _, e = New(Config{"http://127.0.0.1", "store", "pinned-model", "loopback"}, f.Manifest, f.Binding, f.Expected, func(context.Context) (string, error) { return "secret", nil }); e != contract.ErrInvalidArgument {
		t.Fatal("accepted altered mapping")
	}
}
