package content

import (
	"os"
	"strings"
	"testing"
)

func TestNoticeCountQueryStopsAtReachablePaginationRange(t *testing.T) {
	status := 1
	where, _ := noticeWhere(ListParams{Status: &status}, false)
	query := strings.ToLower(noticeCountQuery(where))

	for _, fragment := range []string{
		"select count(*)",
		"from (",
		"select n.id",
		"from bp_notice n",
		"n.status=?",
		"limit ?",
		") counted",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("notice count query lost %q: %s", fragment, query)
		}
	}
}

func TestNoticeListQueryPagesIDsBeforeLoadingHTMLAndCreator(t *testing.T) {
	where, _ := noticeWhere(ListParams{}, false)
	query := strings.ToLower(noticeListQuery(where))

	for _, fragment := range []string{
		"from (",
		"select n.id",
		"from bp_notice n",
		"order by n.is_top desc,n.publish_time desc,n.id desc",
		"limit ? offset ?",
		") filtered",
		"inner join bp_notice n on n.id=filtered.id",
		"n.content_html",
		"left join bp_user u on u.id=n.creator_id",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("notice list query lost %q: %s", fragment, query)
		}
	}

	filterStart := strings.Index(query, "from (")
	filterEnd := strings.Index(query, ") filtered")
	if filterStart < 0 || filterEnd < filterStart {
		t.Fatalf("notice list query lost filtered ID subquery: %s", query)
	}
	filterQuery := query[filterStart:filterEnd]
	for _, forbidden := range []string{"content_html", "bp_user", "date_format"} {
		if strings.Contains(filterQuery, forbidden) {
			t.Fatalf("notice ID page unnecessarily loads %q: %s", forbidden, filterQuery)
		}
	}
}

func TestFriendLinkCountQueryStopsAtReachablePaginationRange(t *testing.T) {
	status := 1
	where, _ := friendLinkWhere(ListParams{Status: &status}, false)
	query := strings.ToLower(friendLinkCountQuery(where))

	for _, fragment := range []string{
		"select count(*)",
		"from (",
		"select l.id",
		"from bp_friend_link l",
		"l.status=?",
		"limit ?",
		") counted",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("friend-link count query lost %q: %s", fragment, query)
		}
	}
}

func TestFriendLinkListQueryPagesIDsBeforeLoadingURLs(t *testing.T) {
	where, _ := friendLinkWhere(ListParams{}, false)
	query := strings.ToLower(friendLinkListQuery(where))

	for _, fragment := range []string{
		"from (",
		"select l.id",
		"from bp_friend_link l",
		"order by l.sort_no asc,l.id asc",
		"limit ? offset ?",
		") filtered",
		"inner join bp_friend_link l on l.id=filtered.id",
		"l.normalized_site_url",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("friend-link list query lost %q: %s", fragment, query)
		}
	}

	filterStart := strings.Index(query, "from (")
	filterEnd := strings.Index(query, ") filtered")
	if filterStart < 0 || filterEnd < filterStart {
		t.Fatalf("friend-link list query lost filtered ID subquery: %s", query)
	}
	filterQuery := query[filterStart:filterEnd]
	for _, forbidden := range []string{"normalized_site_url", "logo_url", "description", "applicant_email"} {
		if strings.Contains(filterQuery, forbidden) {
			t.Fatalf("friend-link ID page unnecessarily loads %q: %s", forbidden, filterQuery)
		}
	}
}

func TestContentPagedReadsRunCountAndPageQueriesConcurrently(t *testing.T) {
	contents, err := os.ReadFile("repository.go")
	if err != nil {
		t.Fatalf("read content repository: %v", err)
	}
	source := string(contents)
	for _, name := range []string{"ListNotices", "ListFriendLinks", "ListApplications"} {
		start := strings.Index(source, "func (r *Repository) "+name)
		if start < 0 {
			t.Fatalf("cannot find %s implementation", name)
		}
		end := strings.Index(source[start+1:], "func ")
		if end >= 0 {
			end += start + 1
		} else {
			end = len(source)
		}
		function := source[start:end]
		if !strings.Contains(function, "dbx.RunParallel(") {
			t.Fatalf("%s must run count and page queries concurrently: %s", name, function)
		}
	}
}
