package content

import (
	"strings"
	"testing"
)

func TestNoticeReaderPathsExpireContent(t *testing.T) {
	publicWhere, _ := noticeWhere(ListParams{}, true)
	for name, query := range map[string]string{
		"public list where": publicWhere,
		"popup":             noticePopupQuery(5),
		"popup count":       noticePopupCountQuery(),
	} {
		lower := strings.ToLower(query)
		for _, fragment := range []string{"n.status=1", "n.publish_time<=now()", "n.expire_time is null", "n.expire_time>now()"} {
			if !strings.Contains(lower, fragment) {
				t.Fatalf("%s lost the live filter %q: %s", name, fragment, query)
			}
		}
	}
}

func TestNoticeKeywordSearchSkipsStoredHTML(t *testing.T) {
	where, args := noticeWhere(ListParams{Keyword: "天气"}, false)
	if strings.Contains(where, "content_html") {
		t.Fatalf("keyword search still scans the html body: %s", where)
	}
	if len(args) != 2 {
		t.Fatalf("keyword should bind title and summary only, got %d args", len(args))
	}
}

func TestNoticeListKeepsPlacementFilters(t *testing.T) {
	ticker := 1
	where, args := noticeWhere(ListParams{IsHomeTicker: &ticker}, false)
	if !strings.Contains(where, "n.is_home_ticker=?") || len(args) != 1 || args[0] != 1 {
		t.Fatalf("home ticker filter not bound: %s %#v", where, args)
	}
}

func TestPopupQueryPagesABoundedCarousel(t *testing.T) {
	if !strings.Contains(noticePopupQuery(maxPopupNotices), "limit ?") {
		t.Fatalf("popup query must bound the carousel payload")
	}
	if strings.Contains(strings.ToLower(noticePopupCountQuery()), "limit") {
		t.Fatalf("popup count must not inherit the carousel bound: %s", noticePopupCountQuery())
	}
}

func TestNormalizeNoticeAcceptsBothExpiryFormats(t *testing.T) {
	for _, value := range []string{"2026-11-04 10:30:00", "2026-11-04T10:30:00+08:00"} {
		item, err := normalizeNotice(SaveNoticeRequest{ContentHTML: "<p>正文</p>", ExpireTime: value, Title: "标题"})
		if err != nil {
			t.Fatalf("expiry %q rejected: %v", value, err)
		}
		if item.ExpireTime != "2026-11-04 10:30:00" {
			t.Fatalf("expiry %q not canonicalised: %q", value, item.ExpireTime)
		}
	}
	if _, err := normalizeNotice(SaveNoticeRequest{ContentHTML: "<p>正文</p>", ExpireTime: "2026-11-04", Title: "标题"}); err == nil {
		t.Fatal("ambiguous expiry accepted")
	}
	item, err := normalizeNotice(SaveNoticeRequest{ContentHTML: "<p>正文</p>", Title: "标题"})
	if err != nil || item.ExpireTime != "" {
		t.Fatalf("missing expiry must stay empty so the row never self-expires: %q %v", item.ExpireTime, err)
	}
}

func TestNormalizeNoticeCarriesHomePlacementAndBounds(t *testing.T) {
	ticker := 1
	item, err := normalizeNotice(SaveNoticeRequest{ContentHTML: "<p>正文</p>", IsHomeTicker: &ticker, Title: "标题"})
	if err != nil || item.IsHomeTicker != 1 {
		t.Fatalf("home ticker flag dropped: %+v %v", item, err)
	}
	other := 2
	if _, err := normalizeNotice(SaveNoticeRequest{ContentHTML: "<p>正文</p>", IsHomeTicker: &other, Title: "标题"}); err == nil {
		t.Fatal("isHomeTicker accepted a value outside 0/1")
	}
	if _, err := normalizeNotice(SaveNoticeRequest{ContentHTML: "<p>正文</p>", Title: strings.Repeat("长", maxNoticeTitleRunes+1)}); err == nil {
		t.Fatal("title longer than the column accepted")
	}
}

func TestHomeTickerNoticeKeepsLiteralTextAsPlainText(t *testing.T) {
	item := homeTickerNotice("<script>alert(1)</script> 上线 & 维护")
	if item.Title != "<script>alert(1)</script> 上线 & 维护" {
		t.Fatalf("marquee title should keep the author's text: %q", item.Title)
	}
	if strings.Contains(strings.ToLower(item.ContentHTML), "<script") {
		t.Fatalf("marquee body carries live markup: %s", item.ContentHTML)
	}
	if !strings.Contains(item.ContentHTML, "&lt;script&gt;") || !strings.Contains(item.ContentHTML, "&amp;") {
		t.Fatalf("marquee body not escaped: %s", item.ContentHTML)
	}
	if item.IsHomeTicker != 1 || item.Status != 1 {
		t.Fatalf("marquee row must occupy the slot and be published: %+v", item)
	}
	if len([]rune(item.Summary)) > 255 {
		t.Fatalf("summary exceeds its column width: %q", item.Summary)
	}
}

func TestHomeTickerReadsKeepPublicLiveButSlotEditable(t *testing.T) {
	// 首页只能读到生效中的滚动文案，所以公开查询必须带生效区间；
	// 编辑面查的是「谁占有投放位」，关掉开关或过期后仍要取回原文，不能丢草稿。
	public := homeTickerPublicReadQuery()
	for _, fragment := range []string{"n.status=1", "n.publish_time<=now()", "n.expire_time>now()"} {
		if !strings.Contains(public, fragment) {
			t.Fatalf("public marquee read lost %q: %s", fragment, public)
		}
	}
	slot := homeTickerSlotReadQuery()
	if !strings.Contains(slot, "n.is_home_ticker=1") || !strings.Contains(slot, "("+noticeLive+")") {
		t.Fatalf("slot read must return the anchored row plus a live flag: %s", slot)
	}
	// 关掉开关不能再撤投放位，否则下一次保存找不到锚点行，只会往公告中心追加新公告。
	if strings.Contains(homeTickerUpdateQuery(false), "is_home_ticker=0") {
		t.Fatal("switching the bar off released the slot anchor")
	}
	if !strings.Contains(homeTickerUpdateQuery(false), "status=0") ||
		!strings.Contains(homeTickerUpdateQuery(true), "status=1") {
		t.Fatalf("ticker switch must move status, not the slot: %q / %q",
			homeTickerUpdateQuery(false), homeTickerUpdateQuery(true))
	}
	if !strings.Contains(homeTickerSlotLookupSQL, "n.is_home_ticker=1") {
		t.Fatalf("slot anchor lookup dropped the placement: %s", homeTickerSlotLookupSQL)
	}
}

func TestPublicNoticePayloadHidesPublisher(t *testing.T) {
	items := publicNoticeItems([]NoticeItem{{CreatorName: "管理员", ID: "1"}, {CreatorName: "运营", ID: "2"}})
	for _, item := range items {
		if item.CreatorName != "" {
			t.Fatalf("reader payload leaked the publisher account name: %+v", item)
		}
	}
	if got := publicNoticeItem(NoticeItem{CreatorName: "管理员", Title: "标题"}).Title; got != "标题" {
		t.Fatalf("public projection dropped reader-visible fields: %q", got)
	}
}
