package content

import (
	"os"
	"strings"
	"testing"
)

func TestFriendLinkApplicationCountStopsAtReachablePaginationRange(t *testing.T) {
	status := applicationFilterPending
	params := ListParams{Status: &status}
	where, args := friendLinkApplicationWhere(params)
	query := strings.ToLower(friendLinkApplicationCountQuery(where))

	for _, fragment := range []string{
		"select count(*)",
		"from (",
		"select a.id",
		"from bp_friend_link_application a",
		"a.status=?",
		"limit ?",
		") counted",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("friend-link application count query lost %q: %s", fragment, query)
		}
	}
	// 状态桶必须作为绑定参数留在 count 上，否则分页总数会统计全部申请。
	if len(args) != 1 || args[0] != friendLinkStatusPending {
		t.Fatalf("count query lost its status argument: %#v", args)
	}
}

// 不认识的状态编号只能匹配不到任何行，不能退化成「不加过滤条件」。
func TestFriendLinkApplicationUnknownStatusMatchesNothing(t *testing.T) {
	unknown := applicationFilterUnknown
	where, args := friendLinkApplicationWhere(ListParams{Status: &unknown})
	if !strings.Contains(where, "a.status=''") {
		t.Fatalf("unknown status filter must narrow to no rows: %s", where)
	}
	if len(args) != 0 {
		t.Fatalf("unknown status filter must not bind arguments: %#v", args)
	}
}

func TestApplicationStatusFilterMapsNamesToStorageCodes(t *testing.T) {
	for raw, want := range map[string]int{
		"PENDING":      applicationFilterPending,
		"approved":     applicationFilterApproved,
		" REJECTED ":   applicationFilterRejected,
		"WITHDRAWN":    applicationFilterWithdrawn,
		"withdrawn":    applicationFilterWithdrawn,
		"NOT_A_STATUS": applicationFilterUnknown,
	} {
		got := applicationStatusFilter(raw)
		if got == nil || *got != want {
			t.Fatalf("status %q mapped to %v, want %d", raw, got, want)
		}
	}
	if got := applicationStatusFilter("  "); got != nil {
		t.Fatalf("blank status must mean no filter, got %v", *got)
	}
}

func TestFriendLinkApplicationListPagesIDsBeforeLoadingWideFields(t *testing.T) {
	params := ListParams{Keyword: "example"}
	where, _ := friendLinkApplicationWhere(params)
	query := strings.ToLower(friendLinkApplicationListQuery(params, where))

	for _, fragment := range []string{
		"from (",
		"select a.id",
		"from bp_friend_link_application a",
		"order by a.id desc",
		"limit ? offset ?",
		") filtered",
		"inner join bp_friend_link_application a on a.id=filtered.id",
		"a.normalized_site_url",
		"a.backlink_url",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("friend-link application list query lost %q: %s", fragment, query)
		}
	}

	filterStart := strings.Index(query, "from (")
	filterEnd := strings.Index(query, ") filtered")
	if filterStart < 0 || filterEnd < filterStart {
		t.Fatalf("friend-link application list query lost filtered ID subquery: %s", query)
	}
	filterQuery := query[filterStart:filterEnd]
	for _, forbidden := range []string{"normalized_site_url", "logo_url", "backlink_url", "review_reason_template"} {
		if strings.Contains(filterQuery, forbidden) {
			t.Fatalf("friend-link application ID page unnecessarily loads %q: %s", forbidden, filterQuery)
		}
	}
}

func TestFriendLinkApplicationStatusListUsesExistingStatusTimeIndexOrder(t *testing.T) {
	status := 0
	params := ListParams{Status: &status}
	where, _ := friendLinkApplicationWhere(params)
	query := strings.ToLower(friendLinkApplicationListQuery(params, where))

	for _, fragment := range []string{
		"select a.id,a.created_at",
		"order by a.created_at desc,a.id desc",
		"order by filtered.created_at desc,filtered.id desc",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("status-filtered friend-link application query lost %q: %s", fragment, query)
		}
	}
}

func TestDonationSupporterCountStopsAtReachablePaginationRange(t *testing.T) {
	where, _ := donationSupporterWhere(ListParams{})
	query := strings.ToLower(donationSupporterCountQuery(where))

	for _, fragment := range []string{
		"select count(*)",
		"from (",
		"select s.id",
		"from bp_donation_supporter s",
		"limit ?",
		") counted",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("donation-supporter count query lost %q: %s", fragment, query)
		}
	}
}

func TestDonationSupporterListPagesIDsBeforeLoadingWideFieldsAndUsers(t *testing.T) {
	where, _ := donationSupporterWhere(ListParams{})
	query := strings.ToLower(donationSupporterListQuery(where))

	for _, fragment := range []string{
		"from (",
		"select s.id",
		"from bp_donation_supporter s",
		"order by s.donated_at desc,s.id desc",
		"limit ? offset ?",
		") filtered",
		"inner join bp_donation_supporter s on s.id=filtered.id",
		"left join bp_user u on u.id=s.created_by",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("donation-supporter list query lost %q: %s", fragment, query)
		}
	}

	filterStart := strings.Index(query, "from (")
	filterEnd := strings.Index(query, ") filtered")
	if filterStart < 0 || filterEnd < filterStart {
		t.Fatalf("donation-supporter list query lost filtered ID subquery: %s", query)
	}
	filterQuery := query[filterStart:filterEnd]
	for _, forbidden := range []string{"avatar_url", "message", "bp_user", "cast(s.amount"} {
		if strings.Contains(filterQuery, forbidden) {
			t.Fatalf("donation-supporter ID page unnecessarily loads %q: %s", forbidden, filterQuery)
		}
	}
}

func TestListDonationSupportersRunsCountAndPageQueriesConcurrently(t *testing.T) {
	contents, err := os.ReadFile("donation_supporter_repository.go")
	if err != nil {
		t.Fatalf("read donation supporter repository: %v", err)
	}
	source := string(contents)
	start := strings.Index(source, "func (r *Repository) ListDonationSupporters")
	end := strings.Index(source, "func donationSupporterWhere")
	if start < 0 || end <= start {
		t.Fatalf("cannot isolate donation supporter list implementation")
	}
	function := source[start:end]
	if !strings.Contains(function, "dbx.RunParallel(") {
		t.Fatalf("donation supporter list must run count and page queries concurrently: %s", function)
	}
}
