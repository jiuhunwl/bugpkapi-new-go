package main

import (
	"io"
	"testing"
)

func TestPartitionCommandValidatesFlagsBeforeDatabaseAccess(t *testing.T) {
	for _, args := range [][]string{{"--months-ahead=-1"}, {"--months-ahead=25"}, {"--timeout=0s"}, {"--allow-rebuild"}, {"unexpected"}} {
		if code := runCallLogPartitionCommand(args, io.Discard); code == 0 {
			t.Fatalf("accepted invalid arguments %v", args)
		}
	}
	if code := runCallLogPartitionCommand([]string{"--help"}, io.Discard); code != 0 {
		t.Fatalf("help returned %d", code)
	}
}
