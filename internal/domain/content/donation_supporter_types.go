package content

// DonationSupporterItem is the public-safe representation of one donation.
// Amount remains a string throughout the API so currency values never pass
// through floating point conversion.
type DonationSupporterItem struct {
	Amount      string `json:"amount"`
	AvatarURL   string `json:"avatarUrl,omitempty"`
	CreateTime  string `json:"createTime,omitempty"`
	CreatorName string `json:"creatorName,omitempty"`
	DonatedAt   string `json:"donatedAt"`
	ID          string `json:"id"`
	Message     string `json:"message,omitempty"`
	Nickname    string `json:"nickname"`
}

type CreateDonationSupporterRequest struct {
	Amount    string `json:"amount"`
	AvatarURL string `json:"avatarUrl"`
	DonatedAt string `json:"donatedAt"`
	Message   string `json:"message"`
	Nickname  string `json:"nickname"`
}
