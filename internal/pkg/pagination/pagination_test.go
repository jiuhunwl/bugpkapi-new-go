package pagination

import (
	"math"
	"testing"
)

func TestNormalizeBoundsPageAndPageSize(t *testing.T) {
	tests := []struct {
		page     int
		pageSize int
		wantPage int
		wantSize int
	}{
		{page: -1, pageSize: -1, wantPage: 1, wantSize: 20},
		{page: 2, pageSize: 50, wantPage: 2, wantSize: 50},
		{page: math.MaxInt, pageSize: math.MaxInt, wantPage: MaxPage, wantSize: 100},
	}
	for _, test := range tests {
		page, size := Normalize(test.page, test.pageSize, 100)
		if page != test.wantPage || size != test.wantSize {
			t.Fatalf("Normalize(%d, %d) = %d/%d, want %d/%d", test.page, test.pageSize, page, size, test.wantPage, test.wantSize)
		}
	}
}

func TestOffsetCannotOverflowFromUntrustedPage(t *testing.T) {
	if got := Offset(math.MaxInt, 100); got != (MaxPage-1)*100 {
		t.Fatalf("Offset() = %d, want %d", got, (MaxPage-1)*100)
	}
}
