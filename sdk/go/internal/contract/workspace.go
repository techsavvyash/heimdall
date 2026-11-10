package contract

type WorkspaceScope struct{ ProductID, Environment, WorkspaceID string }
type WorkspaceRecord struct {
	WorkspaceScope
	Lifecycle            string
	MutationBlocked      bool
	CreatedAt, UpdatedAt int64
}

// WorkspaceGate requires a schema-validated primary read, never a failed read as nil.
func WorkspaceGate(record *WorkspaceRecord, scope WorkspaceScope) (string, error) {
	if record == nil {
		return "deny", nil
	}
	if record.CreatedAt > record.UpdatedAt {
		return "", ErrInvalidArgument
	}
	if record.WorkspaceScope != scope {
		return "", ErrUnavailable
	}
	if record.Lifecycle == "active" && !record.MutationBlocked {
		return "check_relationships", nil
	}
	return "deny", nil
}
