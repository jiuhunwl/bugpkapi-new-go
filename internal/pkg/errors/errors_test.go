package errors

import (
	stderrors "errors"
	"testing"
)

func TestAsBusinessError(t *testing.T) {
	err := Wrap(400, "bad request", stderrors.New("detail"))

	got, ok := AsBusinessError(err)
	if !ok {
		t.Fatal("expected business error")
	}
	if got.StatusCode != 400 || got.Message != "bad request" {
		t.Fatalf("unexpected business error: %+v", got)
	}
}
