package ticket

import (
	"strconv"
	"testing"
)

func TestNormalizeAttachmentIDs(t *testing.T) {
	ids, err := normalizeAttachmentIDs([]string{"3", " 3 ", "", "7"})
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != 3 || ids[1] != 7 {
		t.Fatalf("ids = %#v", ids)
	}
	for _, raw := range []string{"0", "-1", "abc"} {
		if _, err := normalizeAttachmentIDs([]string{raw}); err == nil {
			t.Fatalf("%q accepted", raw)
		}
	}
	tooMany := make([]string, 0, maxTicketAttachments+1)
	for i := 1; i <= maxTicketAttachments+1; i++ {
		tooMany = append(tooMany, strconv.Itoa(i))
	}
	if _, err := normalizeAttachmentIDs(tooMany); err == nil {
		t.Fatal("too many attachments accepted")
	}
}

func TestNormalizeReplyContentAllowsImageOnly(t *testing.T) {
	content, err := normalizeReplyContent("   ", true)
	if err != nil || content != "" {
		t.Fatalf("image-only content = %q err=%v", content, err)
	}
	if _, err := normalizeReplyContent("   ", false); err == nil {
		t.Fatal("empty reply without attachments accepted")
	}
	if preview := attachmentPreview("en"); preview != "[Image]" {
		t.Fatalf("english preview = %q", preview)
	}
	if preview := attachmentPreview("zh"); preview != "[图片]" {
		t.Fatalf("chinese preview = %q", preview)
	}
}
