package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"bugpk-api/internal/pkg/masking"
	"bugpk-api/internal/pkg/smtpmail"
)

func main() {
	host := flag.String("host", "", "SMTP server host")
	port := flag.Int("port", 0, "SMTP server port")
	username := flag.String("username", "", "SMTP username")
	from := flag.String("from", "", "sender email")
	fromName := flag.String("from-name", "BugPk-Api", "sender display name")
	to := flag.String("to", "", "test recipient email")
	useTLS := flag.Bool("tls", false, "use implicit TLS on port 465 or STARTTLS on other ports")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	err := smtpmail.Send(ctx, smtpmail.Config{
		FromEmail: *from,
		FromName:  *fromName,
		Host:      *host,
		Password:  os.Getenv("BUGPK_SMTP_CHECK_PASSWORD"),
		Port:      *port,
		TLS:       *useTLS,
		Timeout:   10 * time.Second,
		Username:  *username,
	}, *to, "BugPk-Api SMTP external verification", "BugPk-Api external SMTP verification message. Sent at "+time.Now().Format(time.RFC3339)+".")
	if err != nil {
		// SMTP libraries commonly include the host/port in dial and TLS errors;
		// do not echo those endpoint details to the terminal.
		fmt.Fprintln(os.Stderr, masking.Error(err))
		os.Exit(1)
	}
	fmt.Println("SMTP_EXTERNAL_VERIFICATION=PASS")
}
