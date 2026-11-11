package contract

// AuditCapacity bounds logical audit records, encoded envelope bytes and sink rows.
// It is not PostgreSQL disk allocation, WAL or replication capacity.
type AuditCapacity struct{ Events, EnvelopeBytes, Deliveries int64 }
type AuditCapacityState struct{ Used, Reserved AuditCapacity }

// TransitionAuditCapacity is pure accounting. Persistence must atomically bind a
// reservation to its decision and deduplicate events before applying a transition.
func TransitionAuditCapacity(limits AuditCapacity, state AuditCapacityState, operation string, charge AuditCapacity) (AuditCapacityState, error) {
	const max int64 = 9007199254740991
	lim := []int64{limits.Events, limits.EnvelopeBytes, limits.Deliveries}
	used := []int64{state.Used.Events, state.Used.EnvelopeBytes, state.Used.Deliveries}
	reserved := []int64{state.Reserved.Events, state.Reserved.EnvelopeBytes, state.Reserved.Deliveries}
	amount := []int64{charge.Events, charge.EnvelopeBytes, charge.Deliveries}
	for _, v := range lim {
		if v < 1 || v > max {
			return AuditCapacityState{}, ErrInvalidArgument
		}
	}
	for i := range lim {
		if used[i] < 0 || reserved[i] < 0 || used[i] > lim[i] || reserved[i] > lim[i]-used[i] {
			return AuditCapacityState{}, ErrAuditUnavailable
		}
	}
	if reserved[0]%2 != 0 || reserved[1]%3000000 != 0 || reserved[2]%64 != 0 || reserved[0]/2 != reserved[1]/3000000 || reserved[0]/2 != reserved[2]/64 || used[0] < reserved[0]/2 || used[1] < used[0] || used[1]/1500000+boolInt(used[1]%1500000 != 0) > used[0] || used[2]/32+boolInt(used[2]%32 != 0) > used[0] {
		return AuditCapacityState{}, ErrAuditUnavailable
	}
	if operation != "append" && operation != "request" && operation != "complete" && operation != "recover" {
		return AuditCapacityState{}, ErrInvalidArgument
	}
	events := int64(1)
	if operation == "recover" {
		events = 2
	}
	if charge.Events != events || charge.EnvelopeBytes < events || charge.EnvelopeBytes > events*1500000 || charge.Deliveries < 0 || charge.Deliveries > events*32 {
		return AuditCapacityState{}, ErrInvalidArgument
	}
	reservation := []int64{2, 3000000, 64}
	for i := range lim {
		var add, remove int64
		if operation == "request" {
			add = reservation[i]
		}
		if operation == "complete" || operation == "recover" {
			remove = reservation[i]
		}
		if reserved[i] < remove {
			return AuditCapacityState{}, ErrAuditUnavailable
		}
		remaining := reserved[i] - remove
		available := lim[i] - used[i] - remaining
		if amount[i] > available || add > available-amount[i] {
			return AuditCapacityState{}, ErrAuditUnavailable
		}
		used[i] += amount[i]
		reserved[i] = remaining + add
	}
	return AuditCapacityState{AuditCapacity{used[0], used[1], used[2]}, AuditCapacity{reserved[0], reserved[1], reserved[2]}}, nil
}

func boolInt(value bool) int64 {
	if value {
		return 1
	}
	return 0
}
