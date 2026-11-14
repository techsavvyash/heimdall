package contract

import (
	"context"
	"errors"
	"regexp"
)

type Code string

const (
	Unauthenticated  Code = "unauthenticated"
	Forbidden        Code = "forbidden"
	InvalidArgument  Code = "invalid_argument"
	NotFound         Code = "not_found"
	Conflict         Code = "conflict"
	Unavailable      Code = "unavailable"
	DeadlineExceeded Code = "deadline_exceeded"
	Cancelled        Code = "cancelled"
	AuditUnavailable Code = "audit_unavailable"
	OutcomeUnknown   Code = "outcome_unknown"
)

var (
	ErrUnauthenticated  = NewError(Unauthenticated, "")
	ErrForbidden        = NewError(Forbidden, "")
	ErrInvalidArgument  = NewError(InvalidArgument, "")
	ErrNotFound         = NewError(NotFound, "")
	ErrConflict         = NewError(Conflict, "")
	ErrUnavailable      = NewError(Unavailable, "")
	ErrDeadlineExceeded = NewError(DeadlineExceeded, "")
	ErrCancelled        = NewError(Cancelled, "")
	ErrAuditUnavailable = NewError(AuditUnavailable, "")
	ErrOutcomeUnknown   = NewError(OutcomeUnknown, "")
)

type Error struct {
	Code        Code   `json:"code"`
	OperationID string `json:"operationId,omitempty"`
}

func (e *Error) Error() string { return string(e.Code) }

func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && e.Code == t.Code
}

var operationIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// NewError retains only known codes and opaque operation IDs, never raw causes.
func NewError(code Code, operationID string) *Error {
	switch code {
	case Unauthenticated, Forbidden, InvalidArgument, NotFound, Conflict,
		Unavailable, DeadlineExceeded, Cancelled, AuditUnavailable, OutcomeUnknown:
	default:
		return &Error{Code: InvalidArgument}
	}
	if operationID != "" && !operationIDPattern.MatchString(operationID) {
		return &Error{Code: InvalidArgument}
	}
	return &Error{Code: code, OperationID: operationID}
}

// TransportFailure classifies missing transport outcomes, not service rejections.
func TransportFailure(reason string, mutationMayExecute bool, operationID string) *Error {
	if mutationMayExecute {
		return NewError(OutcomeUnknown, operationID)
	}
	switch reason {
	case "dependency":
		return NewError(Unavailable, operationID)
	case "deadline":
		return NewError(DeadlineExceeded, operationID)
	case "cancelled":
		return NewError(Cancelled, operationID)
	default:
		return NewError(InvalidArgument, "")
	}
}

func CheckCancellation(ctx context.Context) error {
	err := ctx.Err()
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrDeadlineExceeded
	}
	if err != nil {
		return ErrCancelled
	}
	return nil
}
