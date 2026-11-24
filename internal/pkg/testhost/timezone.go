// Package testhost provides isolated host-zone verification for Go tests.
// It is imported only by test files and never changes a production process.
package testhost

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"
	_ "time/tzdata"
)

const zoneEnv = "BUGPK_TEST_HOST_TIMEZONE"

func Main(m *testing.M) {
	if zone := os.Getenv(zoneEnv); zone != "" {
		location, err := time.LoadLocation(zone)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		time.Local = location
		fmt.Println("HOST_ZONE=" + zone)
	}
	os.Exit(m.Run())
}

func IsChild() bool { return os.Getenv(zoneEnv) != "" }

func Run(t *testing.T, child string) {
	t.Helper()
	// The extreme offsets ensure a date-only host/calendar discrepancy can be
	// observed regardless of the real time of day, without replacing time.Now.
	for _, zone := range []string{"UTC", "Etc/GMT-9", "Etc/GMT+7", "America/New_York", "Etc/GMT+12", "Etc/GMT-14"} {
		t.Run(zone, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.CommandContext(ctx, executable, "-test.run=^"+regexp.QuoteMeta(child)+"$", "-test.v")
			cmd.Env = append(os.Environ(), zoneEnv+"="+zone)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("host=%s: %v\n%s", zone, err, output)
			}
			if !strings.Contains(string(output), "HOST_ZONE="+zone) || !strings.Contains(string(output), "--- PASS: "+child) {
				t.Fatalf("child assertions did not pass: %s", output)
			}
		})
	}
}
