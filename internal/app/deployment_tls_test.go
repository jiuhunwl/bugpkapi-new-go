package app

import (
	"os"
	"strings"
	"testing"
)

func TestDeploymentTemplatesDisableLegacyTLS(t *testing.T) {
	for _, path := range []string{
		"../../deploy/宝塔Nginx配置.conf",
	} {
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, line := range strings.Split(string(contents), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "ssl_protocols ") {
				found = true
				if line != "ssl_protocols TLSv1.2 TLSv1.3;" {
					t.Errorf("unexpected TLS policy in %s: %s", path, line)
				}
			}
		}
		if !found {
			t.Errorf("no TLS protocol policy in %s", path)
		}
	}
}
