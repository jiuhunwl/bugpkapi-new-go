package calloutcome

// SQLClass returns the CASE expression that derives the outcome bucket for a
// call log row. An empty alias produces bare column references; otherwise the
// columns are qualified with "<alias>.".
//
// Rows written before migration 000110 carry outcome_class=0 and are classified
// from success/response_status exactly like Classify. This keeps historical
// rows coherent without a full-table rewrite.
func SQLClass(alias string) string {
	prefix := ""
	if alias != "" {
		prefix = alias + "."
	}
	return `(case
		when ` + prefix + `outcome_class in (1,2,3,4,5) then ` + prefix + `outcome_class
		when ` + prefix + `success=1 then 1
		when coalesce(` + prefix + `response_status,0)=404 then 2
		when coalesce(` + prefix + `response_status,0)=429 then 4
		when coalesce(` + prefix + `response_status,0) between 400 and 499 then 3
		else 5 end)`
}

// SQLLegacyFailure is the historical failure predicate retained for the
// existing failure_count columns and every consistency probe that compares
// them. The new attributed service-failure metric uses the outcome buckets
// instead. Keeping this predicate unchanged during the bucket rollout avoids a
// multi-day replay of the 31GB call log and keeps older projections coherent.
//
// Since 1.2.54 the predicate also excludes rows the outcome classifier marked
// as ExpectedEmpty (an HTTP 2xx response carrying the business "not found"
// code, e.g. {"code":404}): they are normal business results, not failures,
// and previously inflated every failure_count surface (workbench failure rate,
// alert SLO burn, risk rankings) because their HTTP status is 200. Legacy rows
// (outcome_class=0) can never classify as 2 under this predicate — their class
// is derived from success/response_status and status<>404 is already required —
// so historical behavior is preserved exactly.
func SQLLegacyFailure(alias string) string {
	prefix := ""
	if alias != "" {
		prefix = alias + "."
	}
	return `(` + prefix + `success=0 and coalesce(` + prefix + `response_status,0)<>404 and ` + SQLClass(alias) + `<>2)`
}
