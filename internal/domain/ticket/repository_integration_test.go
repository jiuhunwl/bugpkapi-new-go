package ticket

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"bugpk-api/internal/pkg/testdb"

	_ "github.com/go-sql-driver/mysql"
)

func TestTicketRepositoryCRUDAndOwnerIsolationIntegration(t *testing.T) {
	dsn := os.Getenv("BUGPK_MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("BUGPK_MYSQL_TEST_DSN is not configured")
	}
	if err := testdb.ValidateIsolatedMySQLDSN(dsn); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatal(err)
	}

	prefix := fmt.Sprintf("ticket-it-%d", time.Now().UnixNano())
	ownerID := insertTicketTestUser(t, ctx, db, prefix+"-owner")
	otherID := insertTicketTestUser(t, ctx, db, prefix+"-other")
	var ticketID int64
	t.Cleanup(func() {
		if ticketID > 0 {
			_, _ = db.Exec("delete from bp_ticket_message where ticket_id=?", ticketID)
			_, _ = db.Exec("delete from bp_ticket where id=?", ticketID)
		}
		_, _ = db.Exec("delete from bp_user where id in (?,?)", ownerID, otherID)
	})

	repo := NewRepository(db)
	ticketID, err = repo.Create(ctx, newTicketNo(), ownerID, CreateRequest{
		Category: CategoryAPI,
		Content:  "Initial request details",
		Language: "en",
		Priority: PriorityHigh,
		Subject:  "Integration test ticket",
	})
	if err != nil {
		t.Fatal(err)
	}

	ownerDetail, err := repo.Detail(ctx, ticketID, ownerID, false)
	if err != nil {
		t.Fatal(err)
	}
	if ownerDetail.Ticket.UserID != fmt.Sprint(ownerID) || len(ownerDetail.Messages) != 1 {
		t.Fatalf("owner detail = %#v", ownerDetail)
	}
	if _, err := repo.Detail(ctx, ticketID, otherID, false); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("non-owner detail error = %v, want sql.ErrNoRows", err)
	}
	if _, err := repo.Detail(ctx, ticketID, otherID, true); err != nil {
		t.Fatalf("administrator detail failed: %v", err)
	}

	ownerList, err := repo.List(ctx, ownerID, false, ListParams{Page: 1, PageSize: 20})
	if err != nil {
		t.Fatal(err)
	}
	if ownerList.Total != 1 || len(ownerList.Items) != 1 || ownerList.Summary.Unread != 0 {
		t.Fatalf("owner list = %#v", ownerList)
	}
	otherList, err := repo.List(ctx, otherID, false, ListParams{Page: 1, PageSize: 20})
	if err != nil {
		t.Fatal(err)
	}
	if otherList.Total != 0 || len(otherList.Items) != 0 {
		t.Fatalf("non-owner list leaked ticket: %#v", otherList)
	}

	if _, err := repo.AddMessage(ctx, ticketID, otherID, false, "unauthorized reply"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("non-owner reply error = %v, want sql.ErrNoRows", err)
	}
	userReply, err := repo.AddMessage(ctx, ticketID, ownerID, false, "Owner follow-up")
	if err != nil {
		t.Fatal(err)
	}
	if userReply.Status != StatusOpen {
		t.Fatalf("user reply status = %q", userReply.Status)
	}
	staffReply, err := repo.AddMessage(ctx, ticketID, otherID, true, "Support response")
	if err != nil {
		t.Fatal(err)
	}
	if staffReply.Status != StatusWaitingUser {
		t.Fatalf("staff reply status = %q", staffReply.Status)
	}

	adminDetail, err := repo.Detail(ctx, ticketID, otherID, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(adminDetail.Messages) != 3 || adminDetail.Ticket.UnreadCount != 0 {
		t.Fatalf("admin conversation = %#v", adminDetail)
	}
	ownerDetail, err = repo.Detail(ctx, ticketID, ownerID, false)
	if err != nil {
		t.Fatal(err)
	}
	if ownerDetail.Ticket.UnreadCount != 1 {
		t.Fatalf("owner unread = %d, want 1", ownerDetail.Ticket.UnreadCount)
	}
	staleSnapshot := ownerDetail.Ticket
	if _, err := repo.AddMessage(ctx, ticketID, otherID, true, "Same-second support follow-up"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(
		ctx,
		"update bp_ticket set last_message_at=? where id=?",
		staleSnapshot.LastMessageAt,
		ticketID,
	); err != nil {
		t.Fatal(err)
	}
	marked, err := repo.MarkRead(
		ctx,
		ticketID,
		ownerID,
		false,
		staleSnapshot.LastMessageAt,
		staleSnapshot.MessageCount,
	)
	if err != nil || marked {
		t.Fatalf("stale same-second mark read = %v, %v; want false", marked, err)
	}
	ownerDetail, err = repo.Detail(ctx, ticketID, ownerID, false)
	if err != nil {
		t.Fatal(err)
	}
	if ownerDetail.Ticket.UnreadCount != 2 || ownerDetail.Ticket.MessageCount != staleSnapshot.MessageCount+1 {
		t.Fatalf("same-second owner detail = %#v", ownerDetail.Ticket)
	}
	marked, err = repo.MarkRead(
		ctx,
		ticketID,
		ownerID,
		false,
		ownerDetail.Ticket.LastMessageAt,
		ownerDetail.Ticket.MessageCount,
	)
	if err != nil || !marked {
		t.Fatalf("mark read = %v, %v", marked, err)
	}

	ownerIDText := fmt.Sprint(ownerID)
	closed := StatusClosed
	update, err := normalizeUpdateRequest(UpdateRequest{AssignedTo: &ownerIDText, Status: &closed})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Update(ctx, ticketID, otherID, update); err != nil {
		t.Fatal(err)
	}
	updated, err := repo.Detail(ctx, ticketID, otherID, true)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Ticket.Status != StatusClosed || updated.Ticket.AssignedTo != ownerIDText {
		t.Fatalf("updated ticket = %#v", updated.Ticket)
	}
	if updated.Ticket.Language != "en" {
		t.Fatalf("ticket language changed after replies and status update: %q", updated.Ticket.Language)
	}
	if _, err := repo.AddMessage(ctx, ticketID, ownerID, false, "closed reply"); !errors.Is(err, errTicketClosed) {
		t.Fatalf("closed reply error = %v, want errTicketClosed", err)
	}
}

func insertTicketTestUser(t *testing.T, ctx context.Context, db *sql.DB, username string) int64 {
	t.Helper()
	result, err := db.ExecContext(ctx, `
		insert into bp_user(username,password_hash,display_name,status)
		values(?,?,?,1)
	`, username, "integration-test-password-hash", username)
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}
