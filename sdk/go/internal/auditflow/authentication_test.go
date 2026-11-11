package auditflow

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"github.com/techsavvyash/heimdall/sdk/go/internal/identity"
	"io"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type authTransport func(*http.Request) (*http.Response, error)

func (f authTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestAuditedAuthentication(t *testing.T) {
	var f struct {
		Issuer, Audience, Subject string
		Now                       int64
		JWKS                      json.RawMessage
		Vectors                   []struct{ Name, Token string }
	}
	b, e := os.ReadFile("../../../../contracts/fixtures/token-verification.json")
	if e != nil {
		t.Fatal(e)
	}
	if json.Unmarshal(b, &f) != nil {
		t.Fatal("fixture")
	}
	var cases []struct {
		Name, Expected                  string
		Introspection, User             json.RawMessage
		IntrospectionStatus, UserStatus int
	}
	b, e = os.ReadFile("../../../../contracts/fixtures/identity-status.json")
	if e != nil {
		t.Fatal(e)
	}
	if json.Unmarshal(b, &cases) != nil {
		t.Fatal("cases")
	}
	raw := ""
	for _, v := range f.Vectors {
		if v.Name == "valid" {
			raw = v.Token
		}
	}
	old := http.DefaultTransport
	defer func() { http.DefaultTransport = old }()
	config := identity.StatusConfig{Config: identity.Config{Issuer: f.Issuer, Audience: f.Audience, JWKSURI: f.Issuer + "/keys", Profile: "production"}, ProductID: "ledger", Environment: "production", PolicyVersion: "p1", ClientID: "client"}
	setup := func(append Append) *Authentication {
		a, e := NewAuthentication(config, func(context.Context) (string, error) { return "secret", nil }, identity.Gate{Check: func(context.Context, identity.Identity) (bool, error) { return false, nil }, Invalidate: func(context.Context, identity.Negative) error { return nil }}, append, func() time.Time { return time.UnixMilli(f.Now) })
		if e != nil {
			t.Fatal(e)
		}
		return a
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			http.DefaultTransport = authTransport(func(r *http.Request) (*http.Response, error) {
				body, status := c.User, c.UserStatus
				if strings.HasSuffix(r.URL.Path, "/keys") {
					body, status = f.JWKS, 200
				} else if strings.HasSuffix(r.URL.Path, "/introspect") {
					body, status = c.Introspection, c.IntrospectionStatus
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(string(body))), Header: make(http.Header)}, nil
			})
			var events []map[string]any
			a := setup(func(_ context.Context, event map[string]any) (contract.AuditReceipt, error) {
				events = append(events, event)
				return receipt(event), nil
			})
			p, e := a.Verify(context.Background(), raw, "bearer")
			want := "unavailable"
			if c.Expected == "active" {
				want = "succeeded"
				if e != nil || !a.Owns(p) {
					t.Fatal(e)
				}
			} else {
				if c.Expected == "unauthenticated" {
					want = "deny"
				}
				if !errors.Is(e, contract.NewError(contract.Code(c.Expected), "")) {
					t.Fatal(e)
				}
			}
			if len(events) != 1 || events[0]["outcome"] != want {
				t.Fatal(events)
			}
			serialized, _ := json.Marshal(events)
			if strings.Contains(string(serialized), raw) {
				t.Fatal("token leaked")
			}
			if a.Owns(&identity.Principal{}) {
				t.Fatal("fabricated")
			}
		})
	}
	http.DefaultTransport = authTransport(func(r *http.Request) (*http.Response, error) {
		c := cases[0]
		body, status := c.User, c.UserStatus
		if strings.HasSuffix(r.URL.Path, "/keys") {
			body, status = f.JWKS, 200
		} else if strings.HasSuffix(r.URL.Path, "/introspect") {
			body, status = c.Introspection, c.IntrospectionStatus
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(string(body))), Header: make(http.Header)}, nil
	})
	var releases []struct {
		Name, Error string
		AdvanceMs   int64
	}
	b, e = os.ReadFile("../../../../contracts/fixtures/authentication-release.json")
	if e != nil {
		t.Fatal(e)
	}
	if json.Unmarshal(b, &releases) != nil {
		t.Fatal("release fixtures")
	}
	for _, c := range releases {
		t.Run(c.Name, func(t *testing.T) {
			var events []map[string]any
			var clock atomic.Int64
			clock.Store(f.Now)
			a := setup(func(_ context.Context, event map[string]any) (contract.AuditReceipt, error) {
				events = append(events, event)
				clock.Add(c.AdvanceMs)
				return receipt(event), nil
			})
			a.writer.now = func() time.Time { return time.UnixMilli(clock.Load()) }
			p, e := a.Verify(context.Background(), raw, "bearer")
			if c.Error == "" {
				if e != nil || p == nil {
					t.Fatal(e)
				}
			} else if p != nil || !errors.Is(e, contract.NewError(contract.Code(c.Error), "")) {
				t.Fatal(p, e)
			}
			want := 1
			if c.Error != "" {
				want = 2
			}
			if len(events) != want {
				t.Fatal("observation count", len(events))
			}
			if want == 2 && (events[0]["eventId"] == events[1]["eventId"] || events[0]["requestId"] != events[1]["requestId"] || events[1]["reasonCode"] != c.Error) {
				t.Fatal("correction correlation")
			}

		})
	}
	t.Run("wait for durable receipt", func(t *testing.T) {
		entered := make(chan struct{})
		release := make(chan struct{})
		done := make(chan error, 1)
		a := setup(func(_ context.Context, event map[string]any) (contract.AuditReceipt, error) {
			close(entered)
			<-release
			return receipt(event), nil
		})
		go func() { _, e := a.Verify(context.Background(), raw, "bearer"); done <- e }()
		<-entered
		select {
		case <-done:
			t.Fatal("returned before commit")
		default:
		}
		close(release)
		if e := <-done; e != nil {
			t.Fatal(e)
		}
	})
	t.Run("cancel during commit", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		a := setup(func(_ context.Context, event map[string]any) (contract.AuditReceipt, error) {
			cancel()
			return receipt(event), nil
		})
		if p, e := a.Verify(ctx, raw, "bearer"); p != nil || !errors.Is(e, contract.ErrCancelled) {
			t.Fatal(p, e)
		}
	})

	t.Run("failed corrective audit", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		calls := 0
		a := setup(func(_ context.Context, event map[string]any) (contract.AuditReceipt, error) {
			calls++
			if calls == 2 {
				return contract.AuditReceipt{}, errors.New("secret")
			}
			cancel()
			return receipt(event), nil
		})
		if p, e := a.Verify(ctx, raw, "bearer"); p != nil || !errors.Is(e, contract.ErrAuditUnavailable) {
			t.Fatal(p, e)
		}
		if calls != 2 {
			t.Fatal(calls)
		}
	})

	// Signature-invalid and pre-cancelled calls must also have durable terminal records.
	for _, field := range []string{"failure", "productId", "environment", "eventId", "recordedAt", "payloadDigest"} {
		t.Run(field, func(t *testing.T) {
			a := setup(func(_ context.Context, e map[string]any) (contract.AuditReceipt, error) {
				r := receipt(e)
				switch field {
				case "failure":
					return r, errors.New("secret")
				case "productId":
					r.ProductID = "wrong"
				case "environment":
					r.Environment = "wrong"
				case "eventId":
					r.EventID = "wrong"
				case "recordedAt":
					r.RecordedAt++
				case "payloadDigest":
					r.PayloadDigest = "wrong"
				}
				return r, nil
			})
			if p, e := a.Verify(context.Background(), raw, "bearer"); p != nil || !errors.Is(e, contract.ErrAuditUnavailable) {
				t.Fatal(p, e)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var event map[string]any
	a := setup(func(_ context.Context, e map[string]any) (contract.AuditReceipt, error) {
		event = e
		return receipt(e), nil
	})
	if _, e := a.Verify(ctx, raw, "bearer"); !errors.Is(e, contract.ErrCancelled) {
		t.Fatal(e)
	}
	if event["outcome"] != "cancelled" {
		t.Fatal(event)
	}
}
