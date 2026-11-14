package contract

// RetirementCursor is trusted maintenance state for one sweep.
type RetirementCursor struct {
	ProductID   string  `json:"productId"`
	Environment string  `json:"environment"`
	TenantID    *string `json:"tenantId"`
	Now         int64   `json:"now"`
	RetainForMS int64   `json:"retainForMs"`
	EventID     string  `json:"eventId"`
}

type RetirementPage struct {
	EventIDs []string          `json:"eventIds"`
	Next     *RetirementCursor `json:"next"`
}
