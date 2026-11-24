package translation

import "bugpk-api/internal/pkg/i18n"

type columnField struct {
	Name     string
	Column   string
	MaxBytes int
	HTML     bool
}

type resourceSpec struct {
	Type          string
	LabelKey      string
	Table         string
	KeyColumn     string
	KeyKind       i18n.KeyKind
	DisplayColumn string
	Fields        []columnField
}

// defaultResourceSpecs 是可翻译资源的编译期白名单。
// 表名与列名永远不来自用户输入。
var defaultResourceSpecs = []resourceSpec{
	{
		Type: "site", LabelKey: "translationAdmin.resources.site",
		Table: "bp_site_config", KeyColumn: "id", KeyKind: i18n.KeySingleton, DisplayColumn: "site_name",
		Fields: []columnField{
			{Name: "siteName", Column: "site_name", MaxBytes: 64},
			{Name: "slogan", Column: "slogan", MaxBytes: 120},
			{Name: "description", Column: "description", MaxBytes: 255},
			{Name: "seoTitle", Column: "seo_title", MaxBytes: 128},
			{Name: "seoKeywords", Column: "seo_keywords", MaxBytes: 512},
		},
	},
	{
		Type: "interface", LabelKey: "translationAdmin.resources.interface",
		Table: "bp_interface_api", KeyColumn: "id", KeyKind: i18n.KeyNumeric, DisplayColumn: "name",
		Fields: []columnField{
			{Name: "name", Column: "name", MaxBytes: 128},
			{Name: "remark", Column: "remark", MaxBytes: 255},
			{Name: "docSummary", Column: "doc_summary", MaxBytes: 500},
			{Name: "docNotice", Column: "doc_notice", MaxBytes: 500},
		},
	},
	{
		Type: "package_global", LabelKey: "translationAdmin.resources.packageGlobal",
		Table: "bp_package_global", KeyColumn: "id", KeyKind: i18n.KeyNumeric, DisplayColumn: "name",
		Fields: []columnField{
			{Name: "name", Column: "name", MaxBytes: 128},
			{Name: "remark", Column: "remark", MaxBytes: 255},
		},
	},
	{
		Type: "package_interface", LabelKey: "translationAdmin.resources.packageInterface",
		Table: "bp_package_interface", KeyColumn: "id", KeyKind: i18n.KeyNumeric, DisplayColumn: "name",
		Fields: []columnField{
			{Name: "name", Column: "name", MaxBytes: 128},
			{Name: "remark", Column: "remark", MaxBytes: 255},
		},
	},
	// 2026-11-04：bp_package_interface_spec 的名称列是 `name`（与迁移/full SQL
	// 一致），此前误写 spec_name 导致管理端资源列表整页 500（Error 1054）。
	// 翻译字段 key 保留 "specName"，与前端及已存翻译行兼容。
	{
		Type: "package_interface_spec", LabelKey: "translationAdmin.resources.packageInterfaceSpec",
		Table: "bp_package_interface_spec", KeyColumn: "id", KeyKind: i18n.KeyNumeric, DisplayColumn: "name",
		Fields: []columnField{
			{Name: "specName", Column: "name", MaxBytes: 128},
		},
	},
	{
		Type: "package_point", LabelKey: "translationAdmin.resources.packagePoint",
		Table: "bp_package_point", KeyColumn: "id", KeyKind: i18n.KeyNumeric, DisplayColumn: "name",
		Fields: []columnField{
			{Name: "name", Column: "name", MaxBytes: 128},
			{Name: "remark", Column: "remark", MaxBytes: 255},
		},
	},
	{
		Type: "notice", LabelKey: "translationAdmin.resources.notice",
		Table: "bp_notice", KeyColumn: "id", KeyKind: i18n.KeyNumeric, DisplayColumn: "title",
		Fields: []columnField{
			// title also carries the homepage marquee line, so it keeps the
			// 300-character width bp_notice.title was widened to.
			{Name: "title", Column: "title", MaxBytes: 300},
			{Name: "summary", Column: "summary", MaxBytes: 255},
			{Name: "contentHtml", Column: "content_html", MaxBytes: 1 << 20, HTML: true},
		},
	},
	{
		Type: "advertisement", LabelKey: "translationAdmin.resources.advertisement",
		Table: "bp_advertisement", KeyColumn: "id", KeyKind: i18n.KeyNumeric, DisplayColumn: "title",
		Fields: []columnField{
			{Name: "title", Column: "title", MaxBytes: 128},
			{Name: "description", Column: "description", MaxBytes: 512},
		},
	},
	{
		Type: "donation", LabelKey: "translationAdmin.resources.donation",
		Table: "bp_donation_config", KeyColumn: "id", KeyKind: i18n.KeySingleton, DisplayColumn: "title",
		Fields: []columnField{
			{Name: "eyebrow", Column: "eyebrow", MaxBytes: 64},
			{Name: "title", Column: "title", MaxBytes: 120},
			{Name: "description", Column: "description", MaxBytes: 500},
			{Name: "scanTitle", Column: "scan_title", MaxBytes: 120},
			{Name: "scanDescription", Column: "scan_description", MaxBytes: 255},
			{Name: "thanks", Column: "thanks", MaxBytes: 255},
		},
	},
	{
		Type: "recharge_preset", LabelKey: "translationAdmin.resources.rechargePreset",
		Table: "bp_recharge_preset", KeyColumn: "id", KeyKind: i18n.KeyNumeric, DisplayColumn: "name",
		Fields: []columnField{
			{Name: "name", Column: "name", MaxBytes: 128},
		},
	},
	{
		Type: "friend_link", LabelKey: "translationAdmin.resources.friendLink",
		Table: "bp_friend_link", KeyColumn: "id", KeyKind: i18n.KeyNumeric, DisplayColumn: "site_name",
		Fields: []columnField{
			{Name: "description", Column: "description", MaxBytes: 255},
		},
	},
	{
		Type: "redeem_type", LabelKey: "translationAdmin.resources.redeemType",
		Table: "bp_redeem_code_type", KeyColumn: "id", KeyKind: i18n.KeyNumeric, DisplayColumn: "name",
		Fields: []columnField{
			{Name: "name", Column: "name", MaxBytes: 64},
		},
	},
	{
		Type: "role", LabelKey: "translationAdmin.resources.role",
		Table: "bp_role", KeyColumn: "id", KeyKind: i18n.KeyNumeric, DisplayColumn: "role_name",
		Fields: []columnField{
			{Name: "roleName", Column: "role_name", MaxBytes: 64},
		},
	},
}
