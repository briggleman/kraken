// Package cloudflare is a minimal Cloudflare API v4 client for the DNS records
// the Panel creates for game servers. It authenticates with a scoped API token
// (Authorization: Bearer) and implements only what the DNS feature needs: listing
// zones, resolving the zone for a name, and creating/deleting A/CNAME/SRV records.
package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

const defaultBase = "https://api.cloudflare.com/client/v4"

// Client talks to the Cloudflare API with a scoped token.
type Client struct {
	token string
	base  string // overridable in tests
	http  *http.Client
}

// New returns a Client authenticated with the given scoped API token.
func New(token string) *Client {
	return &Client{token: token, base: defaultBase, http: &http.Client{Timeout: 20 * time.Second}}
}

// Zone is a Cloudflare DNS zone (a domain the token can manage).
type Zone struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// envelope is the wrapper Cloudflare puts around every response.
type envelope struct {
	Success bool            `json:"success"`
	Errors  []apiError      `json:"errors"`
	Result  json.RawMessage `json:"result"`
}

type apiError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func joinErrors(errs []apiError) string {
	if len(errs) == 0 {
		return "unknown error"
	}
	parts := make([]string, len(errs))
	for i, e := range errs {
		parts[i] = fmt.Sprintf("%d %s", e.Code, e.Message)
	}
	return strings.Join(parts, "; ")
}

// do performs a request, unwraps the Cloudflare envelope, and decodes Result into out.
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	var env envelope
	if jerr := json.Unmarshal(data, &env); jerr != nil {
		return fmt.Errorf("cloudflare: %s %s: status %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if !env.Success {
		return fmt.Errorf("cloudflare: %s %s: %s", method, path, joinErrors(env.Errors))
	}
	if out != nil && len(env.Result) > 0 {
		return json.Unmarshal(env.Result, out)
	}
	return nil
}

// ListZones returns the zones the token can access. Used by "Test connection" and
// by ZoneFor. Cloudflare paginates; the first page (up to 50) is sufficient here.
func (c *Client) ListZones(ctx context.Context) ([]Zone, error) {
	var zones []Zone
	if err := c.do(ctx, http.MethodGet, "/zones?per_page=50", nil, &zones); err != nil {
		return nil, err
	}
	return zones, nil
}

// ZoneFor returns the zone whose name is the longest suffix of fqdn (so
// a.b.example.com resolves to the example.com zone). Errors if no zone matches.
func (c *Client) ZoneFor(ctx context.Context, fqdn string) (Zone, error) {
	zones, err := c.ListZones(ctx)
	if err != nil {
		return Zone{}, err
	}
	name := strings.ToLower(strings.TrimSuffix(fqdn, "."))
	var best Zone
	for _, z := range zones {
		zn := strings.ToLower(z.Name)
		if name == zn || strings.HasSuffix(name, "."+zn) {
			if len(zn) > len(best.Name) {
				best = z
			}
		}
	}
	if best.ID == "" {
		return Zone{}, fmt.Errorf("cloudflare: no accessible zone matches %q (check the token's zone access)", fqdn)
	}
	return best, nil
}

type dnsRecord struct {
	ID      string   `json:"id,omitempty"`
	Type    string   `json:"type"`
	Name    string   `json:"name"`
	Content string   `json:"content,omitempty"`
	TTL     int      `json:"ttl"`
	Proxied *bool    `json:"proxied,omitempty"`
	Data    *srvData `json:"data,omitempty"`
}

type srvData struct {
	Service  string `json:"service"`
	Proto    string `json:"proto"`
	Name     string `json:"name"`
	Priority int    `json:"priority"`
	Weight   int    `json:"weight"`
	Port     int    `json:"port"`
	Target   string `json:"target"`
}

// CreateHostRecord creates the address record for fqdn: an A record when host is
// an IPv4, AAAA when IPv6, otherwise a CNAME. Always DNS-only (proxied=false) —
// Cloudflare's proxy can't carry raw game traffic. Returns the new record ID.
func (c *Client) CreateHostRecord(ctx context.Context, zoneID, fqdn, host string) (string, error) {
	recType := "CNAME"
	if ip := net.ParseIP(host); ip != nil {
		if ip.To4() != nil {
			recType = "A"
		} else {
			recType = "AAAA"
		}
	}
	proxied := false
	rec := dnsRecord{Type: recType, Name: fqdn, Content: host, TTL: 1, Proxied: &proxied}
	return c.createRecord(ctx, zoneID, rec)
}

// UpdateHostRecord overwrites an existing address record in place (preserving its
// ID), re-pointing it at host — used to fix stale records when a node's public
// host changes. The record type is re-derived from host (A/AAAA/CNAME), so an
// IP↔hostname switch is handled. Always DNS-only.
func (c *Client) UpdateHostRecord(ctx context.Context, zoneID, recordID, fqdn, host string) error {
	recType := "CNAME"
	if ip := net.ParseIP(host); ip != nil {
		if ip.To4() != nil {
			recType = "A"
		} else {
			recType = "AAAA"
		}
	}
	proxied := false
	rec := dnsRecord{Type: recType, Name: fqdn, Content: host, TTL: 1, Proxied: &proxied}
	return c.do(ctx, http.MethodPut, "/zones/"+zoneID+"/dns_records/"+recordID, rec, nil)
}

// CreateSRVRecord creates an SRV record advertising port for fqdn under the given
// service (e.g. "minecraft") and protocol ("tcp"/"udp"). Returns the new record ID.
func (c *Client) CreateSRVRecord(ctx context.Context, zoneID, service, proto, fqdn string, port int) (string, error) {
	service = strings.ToLower(strings.TrimPrefix(service, "_"))
	proto = strings.ToLower(strings.TrimPrefix(proto, "_"))
	rec := dnsRecord{
		Type: "SRV",
		Name: fmt.Sprintf("_%s._%s.%s", service, proto, fqdn),
		TTL:  1,
		Data: &srvData{
			Service:  "_" + service,
			Proto:    "_" + proto,
			Name:     fqdn,
			Priority: 0,
			Weight:   0,
			Port:     port,
			Target:   fqdn,
		},
	}
	return c.createRecord(ctx, zoneID, rec)
}

func (c *Client) createRecord(ctx context.Context, zoneID string, rec dnsRecord) (string, error) {
	var out struct {
		ID string `json:"id"`
	}
	if err := c.do(ctx, http.MethodPost, "/zones/"+zoneID+"/dns_records", rec, &out); err != nil {
		return "", err
	}
	return out.ID, nil
}

// DeleteRecord removes a DNS record. A 404/already-deleted record is not treated
// as an error so cleanup is idempotent.
func (c *Client) DeleteRecord(ctx context.Context, zoneID, recordID string) error {
	err := c.do(ctx, http.MethodDelete, "/zones/"+zoneID+"/dns_records/"+recordID, nil, nil)
	if err != nil && strings.Contains(err.Error(), "81044") { // record does not exist
		return nil
	}
	return err
}
