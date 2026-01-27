// Package countries provides ISO 3166-1 alpha-2 country code validation.
package countries

import "strings"

// ValidCodes contains all valid ISO 3166-1 alpha-2 country codes.
var ValidCodes = map[string]bool{
	"AD": true, // Andorra
	"AE": true, // United Arab Emirates
	"AF": true, // Afghanistan
	"AG": true, // Antigua and Barbuda
	"AI": true, // Anguilla
	"AL": true, // Albania
	"AM": true, // Armenia
	"AO": true, // Angola
	"AQ": true, // Antarctica
	"AR": true, // Argentina
	"AS": true, // American Samoa
	"AT": true, // Austria
	"AU": true, // Australia
	"AW": true, // Aruba
	"AX": true, // Aland Islands
	"AZ": true, // Azerbaijan
	"BA": true, // Bosnia and Herzegovina
	"BB": true, // Barbados
	"BD": true, // Bangladesh
	"BE": true, // Belgium
	"BF": true, // Burkina Faso
	"BG": true, // Bulgaria
	"BH": true, // Bahrain
	"BI": true, // Burundi
	"BJ": true, // Benin
	"BL": true, // Saint Barthelemy
	"BM": true, // Bermuda
	"BN": true, // Brunei
	"BO": true, // Bolivia
	"BQ": true, // Bonaire, Sint Eustatius and Saba
	"BR": true, // Brazil
	"BS": true, // Bahamas
	"BT": true, // Bhutan
	"BV": true, // Bouvet Island
	"BW": true, // Botswana
	"BY": true, // Belarus
	"BZ": true, // Belize
	"CA": true, // Canada
	"CC": true, // Cocos (Keeling) Islands
	"CD": true, // Congo (Democratic Republic)
	"CF": true, // Central African Republic
	"CG": true, // Congo
	"CH": true, // Switzerland
	"CI": true, // Cote d'Ivoire
	"CK": true, // Cook Islands
	"CL": true, // Chile
	"CM": true, // Cameroon
	"CN": true, // China
	"CO": true, // Colombia
	"CR": true, // Costa Rica
	"CU": true, // Cuba
	"CV": true, // Cabo Verde
	"CW": true, // Curacao
	"CX": true, // Christmas Island
	"CY": true, // Cyprus
	"CZ": true, // Czechia
	"DE": true, // Germany
	"DJ": true, // Djibouti
	"DK": true, // Denmark
	"DM": true, // Dominica
	"DO": true, // Dominican Republic
	"DZ": true, // Algeria
	"EC": true, // Ecuador
	"EE": true, // Estonia
	"EG": true, // Egypt
	"EH": true, // Western Sahara
	"ER": true, // Eritrea
	"ES": true, // Spain
	"ET": true, // Ethiopia
	"FI": true, // Finland
	"FJ": true, // Fiji
	"FK": true, // Falkland Islands
	"FM": true, // Micronesia
	"FO": true, // Faroe Islands
	"FR": true, // France
	"GA": true, // Gabon
	"GB": true, // United Kingdom
	"GD": true, // Grenada
	"GE": true, // Georgia
	"GF": true, // French Guiana
	"GG": true, // Guernsey
	"GH": true, // Ghana
	"GI": true, // Gibraltar
	"GL": true, // Greenland
	"GM": true, // Gambia
	"GN": true, // Guinea
	"GP": true, // Guadeloupe
	"GQ": true, // Equatorial Guinea
	"GR": true, // Greece
	"GS": true, // South Georgia and the South Sandwich Islands
	"GT": true, // Guatemala
	"GU": true, // Guam
	"GW": true, // Guinea-Bissau
	"GY": true, // Guyana
	"HK": true, // Hong Kong
	"HM": true, // Heard Island and McDonald Islands
	"HN": true, // Honduras
	"HR": true, // Croatia
	"HT": true, // Haiti
	"HU": true, // Hungary
	"ID": true, // Indonesia
	"IE": true, // Ireland
	"IL": true, // Israel
	"IM": true, // Isle of Man
	"IN": true, // India
	"IO": true, // British Indian Ocean Territory
	"IQ": true, // Iraq
	"IR": true, // Iran
	"IS": true, // Iceland
	"IT": true, // Italy
	"JE": true, // Jersey
	"JM": true, // Jamaica
	"JO": true, // Jordan
	"JP": true, // Japan
	"KE": true, // Kenya
	"KG": true, // Kyrgyzstan
	"KH": true, // Cambodia
	"KI": true, // Kiribati
	"KM": true, // Comoros
	"KN": true, // Saint Kitts and Nevis
	"KP": true, // North Korea
	"KR": true, // South Korea
	"KW": true, // Kuwait
	"KY": true, // Cayman Islands
	"KZ": true, // Kazakhstan
	"LA": true, // Laos
	"LB": true, // Lebanon
	"LC": true, // Saint Lucia
	"LI": true, // Liechtenstein
	"LK": true, // Sri Lanka
	"LR": true, // Liberia
	"LS": true, // Lesotho
	"LT": true, // Lithuania
	"LU": true, // Luxembourg
	"LV": true, // Latvia
	"LY": true, // Libya
	"MA": true, // Morocco
	"MC": true, // Monaco
	"MD": true, // Moldova
	"ME": true, // Montenegro
	"MF": true, // Saint Martin (French part)
	"MG": true, // Madagascar
	"MH": true, // Marshall Islands
	"MK": true, // North Macedonia
	"ML": true, // Mali
	"MM": true, // Myanmar
	"MN": true, // Mongolia
	"MO": true, // Macao
	"MP": true, // Northern Mariana Islands
	"MQ": true, // Martinique
	"MR": true, // Mauritania
	"MS": true, // Montserrat
	"MT": true, // Malta
	"MU": true, // Mauritius
	"MV": true, // Maldives
	"MW": true, // Malawi
	"MX": true, // Mexico
	"MY": true, // Malaysia
	"MZ": true, // Mozambique
	"NA": true, // Namibia
	"NC": true, // New Caledonia
	"NE": true, // Niger
	"NF": true, // Norfolk Island
	"NG": true, // Nigeria
	"NI": true, // Nicaragua
	"NL": true, // Netherlands
	"NO": true, // Norway
	"NP": true, // Nepal
	"NR": true, // Nauru
	"NU": true, // Niue
	"NZ": true, // New Zealand
	"OM": true, // Oman
	"PA": true, // Panama
	"PE": true, // Peru
	"PF": true, // French Polynesia
	"PG": true, // Papua New Guinea
	"PH": true, // Philippines
	"PK": true, // Pakistan
	"PL": true, // Poland
	"PM": true, // Saint Pierre and Miquelon
	"PN": true, // Pitcairn
	"PR": true, // Puerto Rico
	"PS": true, // Palestine
	"PT": true, // Portugal
	"PW": true, // Palau
	"PY": true, // Paraguay
	"QA": true, // Qatar
	"RE": true, // Reunion
	"RO": true, // Romania
	"RS": true, // Serbia
	"RU": true, // Russia
	"RW": true, // Rwanda
	"SA": true, // Saudi Arabia
	"SB": true, // Solomon Islands
	"SC": true, // Seychelles
	"SD": true, // Sudan
	"SE": true, // Sweden
	"SG": true, // Singapore
	"SH": true, // Saint Helena, Ascension and Tristan da Cunha
	"SI": true, // Slovenia
	"SJ": true, // Svalbard and Jan Mayen
	"SK": true, // Slovakia
	"SL": true, // Sierra Leone
	"SM": true, // San Marino
	"SN": true, // Senegal
	"SO": true, // Somalia
	"SR": true, // Suriname
	"SS": true, // South Sudan
	"ST": true, // Sao Tome and Principe
	"SV": true, // El Salvador
	"SX": true, // Sint Maarten (Dutch part)
	"SY": true, // Syria
	"SZ": true, // Eswatini
	"TC": true, // Turks and Caicos Islands
	"TD": true, // Chad
	"TF": true, // French Southern Territories
	"TG": true, // Togo
	"TH": true, // Thailand
	"TJ": true, // Tajikistan
	"TK": true, // Tokelau
	"TL": true, // Timor-Leste
	"TM": true, // Turkmenistan
	"TN": true, // Tunisia
	"TO": true, // Tonga
	"TR": true, // Turkey
	"TT": true, // Trinidad and Tobago
	"TV": true, // Tuvalu
	"TW": true, // Taiwan
	"TZ": true, // Tanzania
	"UA": true, // Ukraine
	"UG": true, // Uganda
	"UM": true, // United States Minor Outlying Islands
	"US": true, // United States
	"UY": true, // Uruguay
	"UZ": true, // Uzbekistan
	"VA": true, // Holy See
	"VC": true, // Saint Vincent and the Grenadines
	"VE": true, // Venezuela
	"VG": true, // Virgin Islands (British)
	"VI": true, // Virgin Islands (U.S.)
	"VN": true, // Vietnam
	"VU": true, // Vanuatu
	"WF": true, // Wallis and Futuna
	"WS": true, // Samoa
	"XK": true, // Kosovo
	"YE": true, // Yemen
	"YT": true, // Mayotte
	"ZA": true, // South Africa
	"ZM": true, // Zambia
	"ZW": true, // Zimbabwe
}

// Normalize trims whitespace and converts to uppercase.
func Normalize(code string) string {
	return strings.ToUpper(strings.TrimSpace(code))
}

// IsValid checks if the given code is a valid ISO 3166-1 alpha-2 country code.
// The code is normalized (trimmed and uppercased) before checking.
func IsValid(code string) bool {
	return ValidCodes[Normalize(code)]
}
