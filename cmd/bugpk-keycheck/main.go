package main

import (
	"flag"
	"fmt"
	"os"

	"bugpk-api/internal/domain/payment"
	"bugpk-api/internal/pkg/masking"
)

const maxKeyFileSize = 1024 * 1024

func main() {
	privatePath := flag.String("private", "", "path to the application RSA private key")
	publicPath := flag.String("public", "", "path to the Alipay RSA public key")
	flag.Parse()

	privateText, err := readKeyFile(*privatePath, "private")
	if err != nil {
		fail(err)
	}
	publicText, err := readKeyFile(*publicPath, "public")
	if err != nil {
		fail(err)
	}
	if err := payment.ValidateAlipayKeyMaterial(privateText, publicText); err != nil {
		fail(err)
	}
	fmt.Println("ALIPAY_KEY_MATERIAL=PASS")
}

func readKeyFile(path, name string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("%s key path is required", name)
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("stat %s key: %w", name, err)
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxKeyFileSize {
		return "", fmt.Errorf("%s key file must be a non-empty regular file no larger than %d bytes", name, maxKeyFileSize)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s key: %w", name, err)
	}
	return string(data), nil
}

func fail(err error) {
	// CLI diagnostics can include filesystem/transport details supplied by an
	// underlying library.  Keep the useful error category while applying the
	// same endpoint/credential redaction used by the service logs.
	fmt.Fprintln(os.Stderr, masking.Error(err))
	os.Exit(1)
}
