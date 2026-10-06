package geoip

import (
	"fmt"
	"net/netip"
	"strings"

	"github.com/oschwald/maxminddb-golang/v2"
)

// Resolver keeps a local MMDB reader open so enrollment never has to call an
// external geolocation service. Lookups are safe to use concurrently.
type Resolver struct {
	database *maxminddb.Reader
}

type locationRecord struct {
	Country struct {
		ISOCode string            `maxminddb:"iso_code"`
		Names   map[string]string `maxminddb:"names"`
	} `maxminddb:"country"`
	City struct {
		Names map[string]string `maxminddb:"names"`
	} `maxminddb:"city"`
}

func Open(path string) (*Resolver, error) {
	database, err := maxminddb.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open GeoIP database: %w", err)
	}
	return &Resolver{database: database}, nil
}

func (r *Resolver) Close() error {
	if r == nil || r.database == nil {
		return nil
	}
	return r.database.Close()
}

// Provider returns "dbip" for DB-IP databases, whose Lite license (CC BY 4.0)
// requires attribution in the console, and "" otherwise.
func (r *Resolver) Provider() string {
	if r != nil && r.database != nil && strings.HasPrefix(strings.ToUpper(r.database.Metadata.DatabaseType), "DBIP") {
		return "dbip"
	}
	return ""
}

func (r *Resolver) Lookup(address string) (string, error) {
	ip, err := netip.ParseAddr(strings.TrimSpace(address))
	if err != nil {
		return "", fmt.Errorf("parse observed IP: %w", err)
	}
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return "", nil
	}
	var record locationRecord
	if err := r.database.Lookup(ip).Decode(&record); err != nil {
		return "", fmt.Errorf("look up observed IP: %w", err)
	}
	return formatRegion(record), nil
}

func formatRegion(record locationRecord) string {
	countryCode := strings.ToUpper(strings.TrimSpace(record.Country.ISOCode))
	city := preferredName(record.City.Names)
	if city != "" && !strings.EqualFold(city, preferredName(record.Country.Names)) {
		if countryCode != "" {
			return countryCode + " · " + city
		}
		return city
	}
	if countryCode != "" {
		return countryCode
	}
	return preferredName(record.Country.Names)
}

func preferredName(names map[string]string) string {
	for _, language := range []string{"zh-CN", "zh", "en"} {
		if value := strings.TrimSpace(names[language]); value != "" {
			return value
		}
	}
	return ""
}
