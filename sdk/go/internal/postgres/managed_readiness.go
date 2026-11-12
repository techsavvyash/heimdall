package postgres

import (
	"regexp"
	"strconv"
	"strings"
)

var managedReadParameter = regexp.MustCompile(`\$[1-7]`)

func managedReadQuery(statement string, args []any, retained bool) (string, []any) {
	if !retained {
		return statement, args
	}
	readiness := managedReadParameter.ReplaceAllStringFunc(managedRetentionReadinessSQL, func(p string) string {
		n, _ := strconv.Atoi(p[1:])
		return "$" + strconv.Itoa(n+len(args))
	})
	statement = strings.ReplaceAll(statement, "NOT pg_is_in_recovery() AND current_setting('transaction_read_only')='off'", "("+readiness+")")
	args = append(args, retentionReadinessArgs()...)
	return statement, append(args, managedChecksum)
}
