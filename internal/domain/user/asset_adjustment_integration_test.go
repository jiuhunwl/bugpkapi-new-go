package user_test

import (
	"context"
	"database/sql"
	"os"
	"sync"
	"testing"

	"bugpk-api/internal/app"
	"bugpk-api/internal/config"
	"bugpk-api/internal/domain/user"
	"bugpk-api/internal/pkg/testdb"

	_ "github.com/go-sql-driver/mysql"
)

func TestAdminUserAssetAdjustmentsAreAuditedIntegration(t *testing.T) {
	if os.Getenv("USER_ASSET_INTEGRATION") != "1" {
		t.Skip("set USER_ASSET_INTEGRATION=1 to run")
	}
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	if err := testdb.ValidateIsolatedMySQLDSN(cfg.Database.DSN()); err != nil {
		t.Fatal(err)
	}
	cfg.Migration.Path = "../../../migrations"
	db, err := sql.Open("mysql", cfg.Database.DSN())
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(8)
	t.Cleanup(func() { _ = db.Close() })
	if err := app.RunMigrations(db, cfg.Migration.Path); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repo := user.NewRepository(db)
	status := 1
	initialPoints := int64(20)
	request := user.SaveUserRequest{
		Balance:       "10.0000",
		PrimaryRoleID: 2,
		Email:         "asset-audit@example.com",
		HomePath:      "/console/workspace",
		Mobile:        "13800000011",
		Points:        &initialPoints,
		RealName:      "Asset Audit User",
		RoleIDs:       []int64{2},
		Status:        &status,
		Username:      "asset_audit_user",
	}
	userID, err := repo.CreateUser(ctx, 1, request, "$2a$10$integration.placeholder.hash", status, initialPoints)
	if err != nil {
		t.Fatal(err)
	}
	assertAssetState(t, db, userID, "10.0000", 20, 1, "10.0000", 20)

	adjustedPoints := int64(5)
	request.Balance = "15.5000"
	request.Points = &adjustedPoints
	if err := repo.UpdateUser(ctx, 1, userID, request, nil, status); err != nil {
		t.Fatal(err)
	}
	assertAssetState(t, db, userID, "15.5000", 5, 2, "15.5000", 5)

	request.RealName = "Asset Audit User Renamed"
	if err := repo.UpdateUser(ctx, 1, userID, request, nil, status); err != nil {
		t.Fatal(err)
	}
	assertAssetState(t, db, userID, "15.5000", 5, 2, "15.5000", 5)

	targets := []struct {
		balance string
		points  int64
	}{
		{balance: "20.0000", points: 10},
		{balance: "30.0000", points: 15},
	}
	start := make(chan struct{})
	errorsCh := make(chan error, len(targets))
	var wait sync.WaitGroup
	wait.Add(len(targets))
	for _, target := range targets {
		target := target
		go func() {
			defer wait.Done()
			<-start
			points := target.points
			concurrentRequest := request
			concurrentRequest.Balance = target.balance
			concurrentRequest.Points = &points
			errorsCh <- repo.UpdateUser(ctx, 1, userID, concurrentRequest, nil, status)
		}()
	}
	close(start)
	wait.Wait()
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatal(err)
		}
	}

	var finalBalance, summedAmount string
	var finalPoints, summedPoints, transactionCount int64
	if err := db.QueryRow("select cast(balance as char),points from bp_user where id=?", userID).Scan(&finalBalance, &finalPoints); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`
		select cast(coalesce(sum(amount),0) as char),coalesce(sum(points),0),count(*)
		from bp_wallet_transaction
		where user_id=? and biz_type in ('ADMIN_USER_INITIAL_ASSET','ADMIN_USER_ASSET_ADJUST')
	`, userID).Scan(&summedAmount, &summedPoints, &transactionCount); err != nil {
		t.Fatal(err)
	}
	if transactionCount != 4 {
		t.Fatalf("asset transaction count = %d, want 4", transactionCount)
	}
	if summedAmount != finalBalance || summedPoints != finalPoints {
		t.Fatalf("asset audit totals amount=%s points=%d, final balance=%s points=%d", summedAmount, summedPoints, finalBalance, finalPoints)
	}
	var wrongOperatorCount int64
	if err := db.QueryRow(`
		select count(*) from bp_wallet_transaction
		where user_id=? and biz_type in ('ADMIN_USER_INITIAL_ASSET','ADMIN_USER_ASSET_ADJUST')
		  and coalesce(biz_id,0) <> 1
	`, userID).Scan(&wrongOperatorCount); err != nil {
		t.Fatal(err)
	}
	if wrongOperatorCount != 0 {
		t.Fatalf("asset transactions without operator id = %d", wrongOperatorCount)
	}

	// A profile/password update must not overwrite assets it did not provide.
	omitted := request
	omitted.Balance = nil
	omitted.Points = nil
	omitted.RealName = "Profile-only edit"
	passwordHash := "$2a$10$sandbox.password-change.fixture"
	if err := repo.UpdateUser(ctx, 1, userID, omitted, &passwordHash, status); err != nil {
		t.Fatal(err)
	}
	assertAssetState(t, db, userID, finalBalance, finalPoints, 4, finalBalance, finalPoints)

	balanceOnly := omitted
	balanceOnly.Balance = "0.0000"
	if err := repo.UpdateUser(ctx, 1, userID, balanceOnly, nil, status); err != nil {
		t.Fatal(err)
	}
	assertAssetState(t, db, userID, "0.0000", finalPoints, 5, "0.0000", finalPoints)

	pointsOnly := omitted
	zero := int64(0)
	pointsOnly.Points = &zero
	if err := repo.UpdateUser(ctx, 1, userID, pointsOnly, nil, status); err != nil {
		t.Fatal(err)
	}
	assertAssetState(t, db, userID, "0.0000", 0, 6, "0.0000", 0)

	// An omitted update racing with an explicit credit must not lose the credit.
	creditPoints := int64(15)
	credit := request
	credit.Balance = "12.5000"
	credit.Points = &creditPoints
	raceStart := make(chan struct{})
	raceErrors := make(chan error, 2)
	for _, update := range []user.SaveUserRequest{omitted, credit} {
		go func(update user.SaveUserRequest) {
			<-raceStart
			raceErrors <- repo.UpdateUser(ctx, 1, userID, update, nil, status)
		}(update)
	}
	close(raceStart)
	for range 2 {
		if err := <-raceErrors; err != nil {
			t.Error(err)
		}
	}
	assertAssetState(t, db, userID, "12.5000", 15, 7, "12.5000", 15)

	// Migration 29 widened wallet storage to decimal(18,4). Narrowing the
	// previous balance in the ledger comparison would fabricate an adjustment.
	largeBalance := omitted
	largeBalance.Balance = "100000000.0000"
	if err := repo.UpdateUser(ctx, 1, userID, largeBalance, nil, status); err != nil {
		t.Fatal(err)
	}
	assertAssetState(t, db, userID, "100000000.0000", 15, 8, "100000000.0000", 15)
	if err := repo.UpdateUser(ctx, 1, userID, omitted, nil, status); err != nil {
		t.Fatal(err)
	}
	assertAssetState(t, db, userID, "100000000.0000", 15, 8, "100000000.0000", 15)
}

func assertAssetState(t *testing.T, db *sql.DB, userID int64, wantBalance string, wantPoints, wantTransactions int64, wantAmount string, wantSummedPoints int64) {
	t.Helper()
	var balance, amount string
	var points, transactionCount, summedPoints int64
	if err := db.QueryRow("select cast(balance as char),points from bp_user where id=?", userID).Scan(&balance, &points); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`
		select cast(coalesce(sum(amount),0) as char),coalesce(sum(points),0),count(*)
		from bp_wallet_transaction
		where user_id=? and biz_type in ('ADMIN_USER_INITIAL_ASSET','ADMIN_USER_ASSET_ADJUST')
	`, userID).Scan(&amount, &summedPoints, &transactionCount); err != nil {
		t.Fatal(err)
	}
	if balance != wantBalance || points != wantPoints || transactionCount != wantTransactions || amount != wantAmount || summedPoints != wantSummedPoints {
		t.Fatalf("asset state balance=%s points=%d tx=%d amount=%s summedPoints=%d", balance, points, transactionCount, amount, summedPoints)
	}
}
