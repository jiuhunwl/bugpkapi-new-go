package interfaceapi

import "encoding/json"

// UnmarshalJSON keeps an omitted timeout distinct from an explicit zero so
// legacy global-policy clients cannot silently reset the current budget.
func (request *SaveDeliveryPolicyRequest) UnmarshalJSON(data []byte) error {
	type plain SaveDeliveryPolicyRequest
	var decoded plain
	payload := struct {
		*plain
		RequestTimeoutMs *int `json:"requestTimeoutMs"`
	}{plain: &decoded}
	if err := json.Unmarshal(data, &payload); err != nil {
		return err
	}
	*request = SaveDeliveryPolicyRequest(decoded)
	request.requestTimeoutMissing = payload.RequestTimeoutMs == nil
	if payload.RequestTimeoutMs != nil {
		request.RequestTimeoutMs = *payload.RequestTimeoutMs
	}
	return nil
}
