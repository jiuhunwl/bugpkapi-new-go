package interfaceapi

import "encoding/json"

// UnmarshalJSON distinguishes an omitted response-check toggle from an explicit
// zero. Only general interface edits use this presence marker; the polling
// editor keeps its replacement semantics.
func (config *UpstreamConfig) UnmarshalJSON(data []byte) error {
	type plain UpstreamConfig
	var decoded plain
	payload := struct {
		*plain
		PollingCheckEnabled *int `json:"pollingCheckEnabled"`
	}{plain: &decoded}
	if err := json.Unmarshal(data, &payload); err != nil {
		return err
	}
	*config = UpstreamConfig(decoded)
	config.pollingCheckMissing = payload.PollingCheckEnabled == nil
	if payload.PollingCheckEnabled != nil {
		config.PollingCheckEnabled = *payload.PollingCheckEnabled
	}
	return nil
}
