package httpstatus

import "testing"

func TestIsSuccessUsesOnlyThe2xxClass(t *testing.T) {
	tests := []struct {
		status int
		want   bool
	}{
		{status: 0, want: false},
		{status: 199, want: false},
		{status: 200, want: true},
		{status: 204, want: true},
		{status: 299, want: true},
		{status: 300, want: false},
		{status: 302, want: false},
		{status: 399, want: false},
		{status: 400, want: false},
		{status: 499, want: false},
		{status: 500, want: false},
		{status: 599, want: false},
	}

	for _, test := range tests {
		if got := IsSuccess(test.status); got != test.want {
			t.Errorf("IsSuccess(%d) = %v, want %v", test.status, got, test.want)
		}
	}
}
