package content

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
	_ "time/tzdata"
)

const donationHostZoneEnv = "BUGPK_DONATION_TEST_HOST_ZONE"

func TestMain(m *testing.M) {
	if zone := os.Getenv(donationHostZoneEnv); zone != "" {
		location, err := time.LoadLocation(zone)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		time.Local = location
	}
	os.Exit(m.Run())
}

func TestExistingDonationTimeContractsAcrossHosts(t *testing.T) {
	for _, zone := range []string{"UTC", "Etc/GMT-9", "Etc/GMT+7", "America/New_York"} {
		t.Run(zone, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.CommandContext(ctx, executable, "-test.run=^TestDonationTimeHostChild$", "-test.v")
			cmd.Env = append(os.Environ(), donationHostZoneEnv+"="+zone)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("host=%s: %v\n%s", zone, err, output)
			}
			if !strings.Contains(string(output), "DONATION_TIME_HOST="+zone) {
				t.Fatal("host assertions did not execute")
			}
		})
	}
}

func TestDonationTimeHostChild(t *testing.T) {
	zone := os.Getenv(donationHostZoneEnv)
	if zone == "" {
		t.Skip("executed by the host-zone parent")
	}
	t.Log("DONATION_TIME_HOST=" + zone)
	t.Run("existing explicit date", TestNormalizeDonationSupporterTrimsAndFormatsFields)
	t.Run("existing default date", TestNormalizeDonationSupporterDefaultsDonationTime)
	t.Run("existing validation", TestNormalizeDonationSupporterRejectsInvalidInput)
	t.Run("UTC clock default", TestDonationDefaultTimeUsesDatabaseBusinessClock)
}
