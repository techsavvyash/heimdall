package contract

import "context"

type ManagedExportScan = func(context.Context, string, *string, *string, int) ([]byte, error)
