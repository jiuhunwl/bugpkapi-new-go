package phoneutil

import "sort"

// maxCountryPrefixLen 是归属桶前缀的最大长度。北美编号计划（NANP）里
// 巴哈马、牙买加等国共用国家码 "1"，只有靠前 4 位才能与美国的 +1 区分开，
// 所以这里匹配到 4 位；返回值语义是"资费与风控归属桶"，不必等同于 ITU 严格国家码。
const maxCountryPrefixLen = 4

// countryCodes 是 ITU-T E.164 首码表的可维护子集。
//
// 表里没有的号段一律返回 known=false，由调用方按默认拒绝处理。宁可漏收录
// 也不要收录错：错把高价国家算进低价档位，等于短信预算失控。
// 后续按发送流水里出现的 known=false 首码补齐即可，加一行数据不动逻辑。
var countryCodes = map[string]string{
	"1":   "US/CA",
	"7":   "RU/KZ",
	"20":  "EG",
	"27":  "ZA",
	"30":  "GR",
	"31":  "NL",
	"32":  "BE",
	"33":  "FR",
	"34":  "ES",
	"36":  "HU",
	"39":  "IT",
	"40":  "RO",
	"41":  "CH",
	"43":  "AT",
	"44":  "GB",
	"45":  "DK",
	"46":  "SE",
	"47":  "NO",
	"48":  "PL",
	"49":  "DE",
	"51":  "PE",
	"52":  "MX",
	"53":  "CU",
	"54":  "AR",
	"55":  "BR",
	"56":  "CL",
	"57":  "CO",
	"58":  "VE",
	"60":  "MY",
	"61":  "AU",
	"62":  "ID",
	"63":  "PH",
	"64":  "NZ",
	"65":  "SG",
	"66":  "TH",
	"81":  "JP",
	"82":  "KR",
	"84":  "VN",
	"86":  "CN",
	"90":  "TR",
	"91":  "IN",
	"92":  "PK",
	"93":  "AF",
	"94":  "LK",
	"95":  "MM",
	"98":  "IR",
	"211": "SS",
	"212": "MA",
	"213": "DZ",
	"216": "TN",
	"218": "LY",
	"220": "SN",
	"221": "GM",
	"222": "MR",
	"223": "ML",
	"224": "GN",
	"225": "CI",
	"226": "BF",
	"227": "NE",
	"228": "TG",
	"229": "BJ",
	"230": "MU",
	"231": "LR",
	"232": "SL",
	"233": "GH",
	"234": "NG",
	"235": "TD",
	"236": "CF",
	"237": "CM",
	"238": "CV",
	"239": "ST",
	"240": "GQ",
	"241": "GA",
	"242": "CG",
	"243": "CD",
	"244": "AO",
	"245": "GW",
	"248": "SC",
	"249": "SD",
	"250": "RW",
	"251": "ET",
	"252": "SO",
	"253": "DJ",
	"254": "KE",
	"255": "TZ",
	"256": "UG",
	"257": "BI",
	"258": "MZ",
	"260": "ZM",
	"261": "MG",
	"263": "ZW",
	"264": "NA",
	"265": "MW",
	"266": "LS",
	"267": "BW",
	"268": "SZ",
	"269": "KM",
	"291": "ER",
	"297": "AW",
	"298": "FO",
	"299": "GL",
	"350": "GI",
	"351": "PT",
	"352": "LU",
	"353": "IE",
	"354": "IS",
	"355": "AL",
	"356": "MT",
	"357": "CY",
	"358": "FI",
	"359": "BG",
	"370": "LT",
	"371": "LV",
	"372": "EE",
	"373": "MD",
	"374": "AM",
	"375": "BY",
	"376": "AD",
	"377": "MC",
	"380": "UA",
	"381": "RS",
	"382": "ME",
	"383": "XK",
	"385": "HR",
	"386": "SI",
	"387": "BA",
	"389": "MK",
	"420": "CZ",
	"421": "SK",
	"423": "LI",
	"500": "FK",
	"501": "BZ",
	"502": "GT",
	"503": "SV",
	"504": "HN",
	"505": "NI",
	"506": "CR",
	"507": "PA",
	"508": "PM",
	"590": "GP",
	"591": "BO",
	"592": "GY",
	"593": "EC",
	"594": "GF",
	"595": "PY",
	"596": "MQ",
	"597": "SR",
	"598": "UY",
	"599": "CW",
	"670": "TL",
	"673": "BN",
	"675": "PG",
	"676": "TO",
	"677": "SB",
	"678": "VU",
	"679": "FJ",
	"685": "WS",
	"686": "KI",
	"687": "NC",
	"688": "TV",
	"689": "PF",
	"850": "KP",
	"852": "HK",
	"853": "MO",
	"855": "KH",
	"856": "LA",
	"880": "BD",
	"886": "TW",
	"960": "MV",
	"961": "LB",
	"962": "JO",
	"963": "SY",
	"964": "IQ",
	"965": "KW",
	"966": "SA",
	"967": "YE",
	"968": "OM",
	"970": "PS",
	"971": "AE",
	"972": "IL",
	"973": "BH",
	"974": "QA",
	"975": "BT",
	"976": "MN",
	"977": "NP",
	"992": "TJ",
	"993": "TM",
	"994": "AZ",
	"995": "GE",
	"996": "KG",
	"998": "UZ",
	// NANP 内非美国/加拿大的目的地：共用首码 1，必须靠前 4 位区分，
	// 否则巴哈马号码会被算进美国的低价档位。
	"1242": "BS",
	"1246": "BB",
	"1264": "AI",
	"1268": "AG",
	"1340": "VI",
	"1345": "KY",
	"1441": "BM",
	"1473": "GD",
	"1649": "TC",
	"1664": "MS",
	"1671": "GU",
	"1721": "SX",
	"1758": "LC",
	"1767": "DM",
	"1784": "VC",
	"1809": "DO",
	"1829": "DO",
	"1849": "DO",
	"1868": "TT",
	"1876": "JM",
}

// lookupCountryCode 用最长前缀匹配定位归属桶。
func lookupCountryCode(digits string) (code string, known bool) {
	for length := maxCountryPrefixLen; length >= 1; length-- {
		if len(digits) < length {
			continue
		}
		candidate := digits[:length]
		// 4 位前缀只服务 NANP：其它国家的首码最长 3 位，
		// 不做这层限制会把 +861 这类正常号码误判成未知。
		if length == 4 && digits[0] != '1' {
			continue
		}
		if _, ok := countryCodes[candidate]; ok {
			return candidate, true
		}
	}
	return "", false
}

// SupportedCountryCodes 返回已收录的归属桶首码（升序），供管理端白名单下拉使用。
func SupportedCountryCodes() []string {
	codes := make([]string, 0, len(countryCodes))
	for code := range countryCodes {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	return codes
}

// CountryName 返回首码对应的地区标签，未知返回空串。
func CountryName(code string) string {
	return countryCodes[ParseCountryCode(code)]
}
