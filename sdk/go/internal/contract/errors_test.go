package contract

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"
)

func TestErrorCodes(t *testing.T) {
	data, err := os.ReadFile("../../../../contracts/fixtures/errors/codes-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var codes []Code
	if err := json.Unmarshal(data, &codes); err != nil {
		t.Fatal(err)
	}
	if len(codes) != 10 {
		t.Fatal("incomplete code corpus")
	}
	for _, code := range codes {
		e := NewError(code, "")
		if e.Code != code {
			t.Fatalf("unsupported code %s", code)
		}
		if !errors.Is(fmt.Errorf("wrapped: %w", e), NewError(code, "")) {
			t.Fatalf("errors.Is failed for %s", code)
		}
		if code != OutcomeUnknown && errors.Is(e, ErrOutcomeUnknown) {
			t.Fatal("unrelated errors matched")
		}
	}
}

func TestTransportFailures(t *testing.T) {
	data, err := os.ReadFile("../../../../contracts/fixtures/errors/transport-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name, Reason, OperationID string
		MutationMayExecute        bool
		Expected                  Error
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) != 6 {
		t.Fatal("incomplete transport corpus")
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			got := TransportFailure(c.Reason, c.MutationMayExecute, c.OperationID)
			if *got != c.Expected {
				t.Fatalf("got %+v, expected %+v", got, c.Expected)
			}
		})
	}
}

func TestCancellation(t *testing.T) {
	if err := CheckCancellation(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(errors.New("Bearer private-token"))
	err := CheckCancellation(ctx)
	if !errors.Is(err, ErrCancelled) {
		t.Fatal(err)
	}
	data, marshalErr := json.Marshal(err)
	if marshalErr != nil || string(data) != `{"code":"cancelled"}` {
		t.Fatalf("unsafe error: %s", data)
	}
	expired, stop := context.WithDeadline(context.Background(), time.Unix(0, 0))
	defer stop()
	if !errors.Is(CheckCancellation(expired), ErrDeadlineExceeded) {
		t.Fatal("missing deadline")
	}
}

func TestInvalidErrorMetadata(t *testing.T) {
	for _, e := range []*Error{NewError(OutcomeUnknown, "Bearer private-token"), NewError(Code("private token"), "")} {
		data, err := json.Marshal(e)
		if err != nil || string(data) != `{"code":"invalid_argument"}` {
			t.Fatalf("unsafe error: %s", data)
		}
	}
}
