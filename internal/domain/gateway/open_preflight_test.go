package gateway

import (
	"context"
	"testing"
)

func TestLoadOpenPreflightRunsIndependentLoadsConcurrently(t *testing.T) {
	apiStarted := make(chan struct{})
	rulesStarted := make(chan struct{})
	release := make(chan struct{})

	resultCh := make(chan struct {
		api      InterfaceConfig
		rules    []string
		apiErr   error
		rulesErr error
	}, 1)
	go func() {
		api, rules, apiErr, rulesErr := loadOpenPreflight(
			context.Background(),
			func(context.Context) (InterfaceConfig, error) {
				close(apiStarted)
				<-release
				return InterfaceConfig{APICode: "weather"}, nil
			},
			func(context.Context) ([]string, error) {
				close(rulesStarted)
				<-release
				return []string{"203.0.113.10"}, nil
			},
		)
		resultCh <- struct {
			api      InterfaceConfig
			rules    []string
			apiErr   error
			rulesErr error
		}{api, rules, apiErr, rulesErr}
	}()

	<-apiStarted
	<-rulesStarted
	close(release)
	result := <-resultCh
	if result.api.APICode != "weather" || len(result.rules) != 1 {
		t.Fatalf("unexpected preflight result: %+v", result)
	}
	if result.apiErr != nil || result.rulesErr != nil {
		t.Fatalf("preflight returned errors: api=%v rules=%v", result.apiErr, result.rulesErr)
	}
}
