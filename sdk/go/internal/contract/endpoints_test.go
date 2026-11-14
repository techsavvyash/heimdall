package contract

import (
	"errors"
	"testing"
)

func TestEndpoints(t *testing.T) {
	var cases []struct {
		Name, URL, Profile string
		Valid              bool
	}
	readAuditFixture(t, "endpoints", &cases)
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			err := ValidateEndpoint(c.URL, c.Profile)
			if c.Valid && err != nil {
				t.Fatal(err)
			}
			if !c.Valid && !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("expected invalid_argument, got %v", err)
			}
		})
	}
	c := EndpointConfig{Issuer: "https://id.example"}
	c.OpenFGA.URL = "http://localhost"
	if !errors.Is(ValidateConfigEndpoints(c), ErrInvalidArgument) {
		t.Fatal("insecure default")
	}
	c.Issuer = "http://localhost"
	c.OpenFGA.URL = "https://fga.example"
	if !errors.Is(ValidateConfigEndpoints(c), ErrInvalidArgument) {
		t.Fatal("insecure issuer")
	}
	c.TransportProfile = "loopback"
	c.OpenFGA.URL = "http://127.0.0.1:8081"
	if err := ValidateConfigEndpoints(c); err != nil {
		t.Fatal(err)
	}
}
