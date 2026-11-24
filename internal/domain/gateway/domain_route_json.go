package gateway

import (
	"bytes"
	"encoding/json"

	"bugpk-api/internal/pkg/domainrouting"
)

// UnmarshalJSON records whether priority was present so the API can support
// both the documented default and an explicit priority of zero. The marker is
// intentionally private and is never returned to clients.
func (route *DomainPollingRoute) UnmarshalJSON(data []byte) error {
	type plain DomainPollingRoute
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	*route = DomainPollingRoute(decoded)
	raw, present := fields["priority"]
	route.prioritySet = present && !bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
	if !route.prioritySet {
		route.Priority = domainrouting.DefaultPriority
	}
	return nil
}
