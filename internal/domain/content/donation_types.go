package content

// DonationMethod describes one public donation channel. The key is stable so
// the administrator can reorder or rename a channel without changing its
// identity in the frontend.
type DonationMethod struct {
	Color     string `json:"color"`
	Enabled   int    `json:"enabled"`
	Icon      string `json:"icon"`
	Key       string `json:"key"`
	Name      string `json:"name"`
	QRCodeURL string `json:"qrCodeUrl"`
	SortNo    int    `json:"sortNo"`
	Tip       string `json:"tip"`
}

// DonationConfig is the singleton administrator-facing configuration.
type DonationConfig struct {
	Description     string           `json:"description"`
	Enabled         int              `json:"enabled"`
	Eyebrow         string           `json:"eyebrow"`
	ID              int64            `json:"id,omitempty"`
	Methods         []DonationMethod `json:"methods"`
	ScanDescription string           `json:"scanDescription"`
	ScanTitle       string           `json:"scanTitle"`
	Thanks          string           `json:"thanks"`
	Title           string           `json:"title"`
	UpdateTime      string           `json:"updateTime,omitempty"`
}

// PublicDonationConfig omits unpublished methods and database-only metadata.
type PublicDonationConfig struct {
	Description     string           `json:"description"`
	Enabled         int              `json:"enabled"`
	Eyebrow         string           `json:"eyebrow"`
	Methods         []DonationMethod `json:"methods"`
	ScanDescription string           `json:"scanDescription"`
	ScanTitle       string           `json:"scanTitle"`
	Thanks          string           `json:"thanks"`
	Title           string           `json:"title"`
}

type SaveDonationConfigRequest struct {
	Description     string           `json:"description"`
	Enabled         int              `json:"enabled"`
	Eyebrow         string           `json:"eyebrow"`
	Methods         []DonationMethod `json:"methods"`
	ScanDescription string           `json:"scanDescription"`
	ScanTitle       string           `json:"scanTitle"`
	Thanks          string           `json:"thanks"`
	Title           string           `json:"title"`
}
