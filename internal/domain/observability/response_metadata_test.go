package observability

import "testing"

func TestApplyResponseMetadataUsesOriginalAndSavedByteLengths(t *testing.T) {
	item := CallLogItem{ResponseBody: "saved", ResponseSize: 100, ResponseTruncated: true}
	applyResponseMetadata(&item)
	if item.ResponseSaved != 5 || !item.ResponseTruncated {
		t.Fatalf("response metadata = saved:%d truncated:%v", item.ResponseSaved, item.ResponseTruncated)
	}

	complete := CallLogItem{ResponseBody: "完整", ResponseSize: int64(len("完整"))}
	applyResponseMetadata(&complete)
	if complete.ResponseSaved != int64(len("完整")) || complete.ResponseTruncated {
		t.Fatalf("complete response metadata = saved:%d truncated:%v", complete.ResponseSaved, complete.ResponseTruncated)
	}

	binary := CallLogItem{ResponseBody: "[BINARY_RESPONSE_OMITTED]", ResponseSize: 2, ResponseTruncated: true}
	applyResponseMetadata(&binary)
	if !binary.ResponseTruncated {
		t.Fatal("omitted binary response must be marked as truncated")
	}
}

func TestApplyResponseMetadataPreservesDatabaseSavedLengthWithoutBody(t *testing.T) {
	item := CallLogItem{ResponseSaved: 321, ResponseSize: 500, ResponseTruncated: true}
	applyResponseMetadata(&item)
	if item.ResponseSaved != 321 || !item.ResponseTruncated {
		t.Fatalf("database response metadata was overwritten: %+v", item)
	}
}
