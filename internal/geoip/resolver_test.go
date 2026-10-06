package geoip

import "testing"

func TestFormatRegionPrefersChineseCityAndCountryCode(t *testing.T) {
	var record locationRecord
	record.Country.ISOCode = "hk"
	record.Country.Names = map[string]string{"en": "Hong Kong", "zh-CN": "香港"}
	record.City.Names = map[string]string{"en": "Hong Kong", "zh-CN": "香港"}
	if got := formatRegion(record); got != "HK" {
		t.Fatalf("formatRegion() = %q, want HK", got)
	}

	record.Country.ISOCode = "in"
	record.Country.Names = map[string]string{"en": "India", "zh-CN": "印度"}
	record.City.Names = map[string]string{"en": "Mumbai", "zh-CN": "孟买"}
	if got := formatRegion(record); got != "IN · 孟买" {
		t.Fatalf("formatRegion() = %q, want IN · 孟买", got)
	}
}
