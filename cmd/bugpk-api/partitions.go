package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"bugpk-api/internal/app"
	"bugpk-api/internal/config"
	"bugpk-api/internal/pkg/businessclock"
	"bugpk-api/internal/pkg/logpartition"
	"bugpk-api/internal/pkg/masking"
)

func runCallLogPartitionCommand(args []string, output io.Writer) int {
	flags := flag.NewFlagSet("call-log-partitions", flag.ContinueOnError)
	flags.SetOutput(output)
	apply := flags.Bool("apply", false, "execute the plan (default: read-only SQL preview)")
	rebuild := flags.Bool("allow-rebuild", false, "authorize initial full table rebuild after backup and stopping writers")
	ahead := flags.Int("months-ahead", 2, "provision this many future calendar months, 0-24")
	timeout := flags.Duration("timeout", 30*time.Minute, "maximum duration; initial conversion may take a long time")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 1
	}
	if flags.NArg() != 0 || *ahead < 0 || *ahead > 24 || *timeout <= 0 || (*rebuild && !*apply) {
		fmt.Fprintln(output, "invalid partition options: months-ahead=0..24, timeout>0, no positional arguments; --allow-rebuild requires --apply")
		return 1
	}
	cfg, err := config.Load(os.Getenv("CONFIG_FILE"))
	if err != nil {
		fmt.Fprintln(output, "configuration error:", masking.Error(err))
		return 1
	}
	// Maintenance uses its own single connection, not the online request pool.
	cfg.Database.ReadTimeout = *timeout
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	var db *sql.DB
	if *apply {
		db, err = app.OpenDatabase(ctx, cfg.Database)
	} else {
		db, err = sql.Open("mysql", cfg.Database.DSN())
	}
	if err != nil {
		fmt.Fprintln(output, "database error:", masking.Error(err))
		return 1
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	fmt.Fprintln(output, "Initial conversion rebuilds the table. Back up and stop writers first. No retention deletion is performed.")
	fmt.Fprintln(output, "Primary key becomes (id,created_at); AUTO_INCREMENT stays table-wide. External imports must preserve globally unique IDs.")
	plan, err := logpartition.Run(ctx, db, logpartition.Options{Now: time.Now().In(businessclock.Location()), MonthsAhead: *ahead, Apply: *apply, AllowRebuild: *rebuild})
	if plan.SQL != "" {
		fmt.Fprintln(output, plan.SQL+";")
	}
	if err != nil {
		fmt.Fprintln(output, "partition operation failed:", masking.Error(err))
		fmt.Fprintln(output, "Inspect MySQL DDL/partition state before retrying after a timeout; do not assume a timed-out ALTER was rolled back.")
		return 1
	}
	switch {
	case plan.SQL == "":
		fmt.Fprintln(output, "Partitions already cover the requested months.")
	case *apply:
		fmt.Fprintln(output, "Partition plan applied.")
	default:
		fmt.Fprintln(output, "Preview only; no schema or data was changed.")
	}
	return 0
}
