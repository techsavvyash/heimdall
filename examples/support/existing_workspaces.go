package main

import (
	"context"
	"database/sql"

	heimdall "github.com/techsavvyash/heimdall/sdk/go"
)

// The product supplies active candidates in stable ID order. OpenFGA filters them for the user.
type existingWorkspaceSource struct{ db *sql.DB }

func (s existingWorkspaceSource) Scan(ctx context.Context, input heimdall.ScanOptions) (heimdall.CandidatePage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name FROM support.workspaces
		WHERE active AND id COLLATE "C" > $1 ORDER BY id COLLATE "C" LIMIT $2`, input.After, input.Limit+1)
	if err != nil {
		return heimdall.CandidatePage{}, err
	}
	defer rows.Close()
	page := heimdall.CandidatePage{Exhausted: true}
	for rows.Next() {
		if len(page.Items) == input.Limit {
			page.Exhausted = false
			break
		}
		var workspace heimdall.Workspace
		if err := rows.Scan(&workspace.ID, &workspace.Name); err != nil {
			return heimdall.CandidatePage{}, err
		}
		page.Items = append(page.Items, heimdall.Candidate{Workspace: workspace, Position: workspace.ID})
	}
	return page, rows.Err()
}
