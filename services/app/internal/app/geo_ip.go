package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

// GeoIPResult is an approximate place from the caller's public IP (city-level).
type GeoIPResult struct {
	OK        bool    `json:"ok"`
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	City      string  `json:"city"`
	Region    string  `json:"region"`
	Country   string  `json:"country"`
	QueryIP   string  `json:"queryIp"`
	Source    string  `json:"source"`
	Message   string  `json:"message,omitempty"`
}

type ipAPIResponse struct {
	Status     string  `json:"status"`
	Message    string  `json:"message"`
	Country    string  `json:"country"`
	RegionName string  `json:"regionName"`
	City       string  `json:"city"`
	Lat        float64 `json:"lat"`
	Lon        float64 `json:"lon"`
	Query      string  `json:"query"`
}

// ResolveGeoIP looks up approximate lat/lon for the client IP via ip-api.com
// (no API key; HTTP; city-level only — not for ~3 km Nearby GPS mode).
func (s *Service) ResolveGeoIP(ctx context.Context, clientIP string) GeoIPResult {
	ip := strings.TrimSpace(clientIP)
	useEgress := ip == "" || isNonPublicIP(ip)

	url := "http://ip-api.com/json/?fields=status,message,country,regionName,city,lat,lon,query"
	if !useEgress {
		url = fmt.Sprintf(
			"http://ip-api.com/json/%s?fields=status,message,country,regionName,city,lat,lon,query",
			ip,
		)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return GeoIPResult{OK: false, Message: "request_failed", Source: "ip-api"}
	}

	client := &http.Client{Timeout: 4 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return GeoIPResult{OK: false, Message: "upstream_unreachable", Source: "ip-api"}
	}
	defer resp.Body.Close()

	var raw ipAPIResponse
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return GeoIPResult{OK: false, Message: "bad_upstream", Source: "ip-api"}
	}
	if !strings.EqualFold(raw.Status, "success") || (raw.Lat == 0 && raw.Lon == 0) {
		msg := strings.TrimSpace(raw.Message)
		if msg == "" {
			msg = "lookup_failed"
		}
		return GeoIPResult{OK: false, Message: msg, Source: "ip-api", QueryIP: raw.Query}
	}

	city := strings.TrimSpace(raw.City)
	region := strings.TrimSpace(raw.RegionName)
	// Prefer a catalog city name when coords land near one of ours.
	if matched := s.matchCityNear(ctx, raw.Lat, raw.Lon, city); matched != "" {
		city = matched
	}

	return GeoIPResult{
		OK:        true,
		Latitude:  raw.Lat,
		Longitude: raw.Lon,
		City:      city,
		Region:    region,
		Country:   strings.TrimSpace(raw.Country),
		QueryIP:   raw.Query,
		Source:    "ip-api",
	}
}

func (s *Service) matchCityNear(ctx context.Context, lat, lon float64, hint string) string {
	if s.DB == nil {
		return strings.TrimSpace(hint)
	}
	rows, err := s.DB.QueryContext(ctx, `
SELECT name, latitude, longitude FROM app_cities
WHERE latitude IS NOT NULL AND longitude IS NOT NULL`)
	if err != nil {
		return strings.TrimSpace(hint)
	}
	defer rows.Close()

	hintLower := strings.ToLower(strings.TrimSpace(hint))
	bestName := ""
	bestKm := 80.0 // city-scale match only
	for rows.Next() {
		var name string
		var clat, clon float64
		if err := rows.Scan(&name, &clat, &clon); err != nil {
			continue
		}
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if hintLower != "" && strings.Contains(strings.ToLower(name), hintLower) {
			return name
		}
		if hintLower != "" && strings.Contains(hintLower, strings.ToLower(name)) {
			return name
		}
		d := haversineKm(lat, lon, clat, clon)
		if d < bestKm {
			bestKm = d
			bestName = name
		}
	}
	if bestName != "" {
		return bestName
	}
	return strings.TrimSpace(hint)
}

// ClientIPFromRequest prefers Traefik / proxy headers, then RemoteAddr.
func ClientIPFromRequest(r *http.Request) string {
	if r == nil {
		return ""
	}
	for _, h := range []string{"X-Forwarded-For", "X-Real-IP", "CF-Connecting-IP"} {
		raw := strings.TrimSpace(r.Header.Get(h))
		if raw == "" {
			continue
		}
		// X-Forwarded-For: client, proxy1, proxy2
		parts := strings.Split(raw, ",")
		for _, p := range parts {
			cand := strings.TrimSpace(p)
			if cand == "" {
				continue
			}
			if host, _, err := net.SplitHostPort(cand); err == nil {
				cand = host
			}
			if net.ParseIP(cand) != nil {
				return cand
			}
		}
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err == nil {
		return host
	}
	return strings.TrimSpace(r.RemoteAddr)
}

func isNonPublicIP(ipStr string) bool {
	ip := net.ParseIP(strings.TrimSpace(ipStr))
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
		return true
	}
	return false
}
