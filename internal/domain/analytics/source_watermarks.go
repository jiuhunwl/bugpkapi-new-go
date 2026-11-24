package analytics

import (
	"context"
	"database/sql"
	"fmt"

	"bugpk-api/internal/pkg/calloutcome"
)

// A cheap ingest probe is deliberately only a recent PRIMARY-key sample.
// Missing failure/risk traffic must NOT scan the entire multi-million-row
// source. Exact older-watermark and uniform-omission checks belong to the
// independent audit, which accumulates them while traversing bounded chunks.
var recentSourceWatermarksQuery = `
 select /*+ MAX_EXECUTION_TIME(1000) */
  max(if(l.charge_type<>'ADMIN' and u.id is not null,l.created_at,null)),
  max(if(l.charge_type<>'ADMIN' and u.id is not null and ` + calloutcome.SQLLegacyFailure("l") + `,l.created_at,null)),
  max(if(l.charge_type<>'ADMIN' and u.id is not null and ` + calloutcome.SQLLegacyFailure("l") + ` and coalesce(l.client_ip,'')<>'',l.created_at,null)),
  max(if(l.charge_type<>'ADMIN' and u.id is not null and coalesce(l.request_fingerprint,'')<>'',l.created_at,null))
 from (select id,created_at,user_id,charge_type,success,response_status,outcome_class,client_ip,request_fingerprint
       from bp_interface_call_log force index(primary) where id<=?
       order by id desc limit ?) l
 left join bp_user u on u.id=l.user_id
 where l.created_at>=date_sub(current_date(),interval 31 day)
`

func probeRecentSourceWatermarks(ctx context.Context, tx *sql.Tx, upper int64) (rollupRepairCoverage, error) {
	var c rollupRepairCoverage
	if err := tx.QueryRowContext(ctx, recentSourceWatermarksQuery, upper, sourceAuditChunkSize).Scan(&c.sourceLatest, &c.failureLatest, &c.failureWithIPLatest, &c.riskLatest); err != nil {
		return c, fmt.Errorf("probe bounded recent source watermarks: %w", err)
	}
	return c, nil
}
