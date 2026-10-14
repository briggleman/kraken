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
	"net/url"
	"regexp"
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
	return NewWithBase(token, defaultBase)
}

// NewWithBase is New against another API root: a test's fake Cloudflare.
func NewWithBase(token, base string) *Client {
	return &Client{token: token, base: strings.TrimRight(base, "/"), http: &http.Client{Timeout: 20 * time.Second}}
}

// Zone is a Cloudflare DNS zone (a domain the token can manage).
type Zone struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// envelope is the wrapper Cloudflare puts around every response.
type envelope struct {
	Success    bool            `json:"success"`
	Errors     []apiError      `json:"errors"`
	Result     json.RawMessage `json:"result"`
	ResultInfo *struct {
		Page       int `json:"page"`
		TotalPages int `json:"total_pages"`
	} `json:"result_info"`
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
	_, err := c.doPage(ctx, method, path, body, out)
	return err
}

// doPage is do that also reports how many pages the listing has (1 when the
// response isn't paginated).
func (c *Client) doPage(ctx context.Context, method, path string, body, out any) (totalPages int, err error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rdr)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	var env envelope
	if jerr := json.Unmarshal(data, &env); jerr != nil {
		return 0, fmt.Errorf("cloudflare: %s %s: status %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if !env.Success {
		return 0, fmt.Errorf("cloudflare: %s %s: %s", method, path, joinErrors(env.Errors))
	}
	totalPages = 1
	if env.ResultInfo != nil && env.ResultInfo.TotalPages > 1 {
		totalPages = env.ResultInfo.TotalPages
	}
	if out != nil && len(env.Result) > 0 {
		return totalPages, json.Unmarshal(env.Result, out)
	}
	return totalPages, nil
}

// maxZonePages bounds the zone listing: 50 pages of 50 is 2,500 zones, far past
// any token a Panel is given, and a misbehaving page count can't spin forever.
const maxZonePages = 50

// ListZones returns every zone the token can access, across pages. Used by
// "Test connection" and by ZoneFor. Reading only the first page used to make a
// token with more than 50 zones fail to find the zone a name belongs to.
func (c *Client) ListZones(ctx context.Context) ([]Zone, error) {
	var zones []Zone
	for page := 1; page <= maxZonePages; page++ {
		var batch []Zone
		total, err := c.doPage(ctx, http.MethodGet, fmt.Sprintf("/zones?per_page=50&page=%d", page), nil, &batch)
		if err != nil {
			return nil, err
		}
		zones = append(zones, batch...)
		if page >= total || len(batch) == 0 {
			break
		}
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

// srvData is an SRV record's data in Cloudflare's current, name-based form:
// the service, protocol and owner name live in the record's name
// ("_enshrouded._udp.play.example.com"), and data carries only the rest. The
// old form also sent service/proto/name inside data, which the API has since
// dropped.
type srvData struct {
	Priority int    `json:"priority"`
	Weight   int    `json:"weight"`
	Port     int    `json:"port"`
	Target   string `json:"target"`
}

func hostRecordType(host string) string {
	if ip := net.ParseIP(host); ip != nil {
		if ip.To4() != nil {
			return "A"
		}
		return "AAAA"
	}
	return "CNAME"
}

// CreateHostRecord creates the address record for fqdn: an A record when host is
// an IPv4, AAAA when IPv6, otherwise a CNAME. Always DNS-only (proxied=false) —
// Cloudflare's proxy can't carry raw game traffic. Returns the new record ID.
func (c *Client) CreateHostRecord(ctx context.Context, zoneID, fqdn, host string) (string, error) {
	proxied := false
	rec := dnsRecord{Type: hostRecordType(host), Name: fqdn, Content: host, TTL: 1, Proxied: &proxied}
	return c.createRecord(ctx, zoneID, rec)
}

// Record is an existing DNS record, the fields a lookup needs.
type Record struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Name string `json:"name"`
}

// FindRecords lists the zone's records named name (any type).
func (c *Client) FindRecords(ctx context.Context, zoneID, name string) ([]Record, error) {
	var out []Record
	q := url.Values{"name": {name}, "per_page": {"100"}}
	if err := c.do(ctx, http.MethodGet, "/zones/"+zoneID+"/dns_records?"+q.Encode(), nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// EnsureHostRecord points fqdn at host, updating the address record that
// already holds the name or creating one. It reports whether it created the
// record, so a publish that fails later can remove only what it added.
//
// A record that already holds the name is adopted, not duplicated: Cloudflare
// refuses a second A/CNAME for the same name, so a record left behind — by an
// earlier publish that failed half-way, or made by hand — used to fail every
// publish after it, and the name could never be saved.
func (c *Client) EnsureHostRecord(ctx context.Context, zoneID, fqdn, host string) (id string, created bool, err error) {
	recs, err := c.FindRecords(ctx, zoneID, fqdn)
	if err != nil {
		return "", false, err
	}
	for _, r := range recs {
		switch r.Type {
		case "A", "AAAA", "CNAME":
			return r.ID, false, c.UpdateHostRecord(ctx, zoneID, r.ID, fqdn, host)
		}
	}
	id, err = c.CreateHostRecord(ctx, zoneID, fqdn, host)
	return id, err == nil, err
}

// UpdateHostRecord overwrites an existing address record in place (preserving its
// ID), re-pointing it at host — used to fix stale records when a node's public
// host changes. The record type is re-derived from host (A/AAAA/CNAME), so an
// IP↔hostname switch is handled. Always DNS-only.
func (c *Client) UpdateHostRecord(ctx context.Context, zoneID, recordID, fqdn, host string) error {
	proxied := false
	rec := dnsRecord{Type: hostRecordType(host), Name: fqdn, Content: host, TTL: 1, Proxied: &proxied}
	return c.do(ctx, http.MethodPut, "/zones/"+zoneID+"/dns_records/"+recordID, rec, nil)
}

// SRVName is the owner name of the SRV record for service and proto at fqdn:
// "_enshrouded._udp.play.example.com". service must already be normalised
// (NormalizeService).
func SRVName(service, proto, fqdn string) string {
	return fmt.Sprintf("_%s._%s.%s", service, strings.ToLower(strings.TrimPrefix(proto, "_")), fqdn)
}

func srvRecord(service, proto, fqdn string, port int) dnsRecord {
	return dnsRecord{
		Type: "SRV",
		Name: SRVName(service, proto, fqdn),
		TTL:  1,
		Data: &srvData{Priority: 0, Weight: 0, Port: port, Target: fqdn},
	}
}

// CreateSRVRecord creates an SRV record advertising port for fqdn under the given
// service (e.g. "minecraft") and protocol ("tcp"/"udp"). Returns the new record ID.
func (c *Client) CreateSRVRecord(ctx context.Context, zoneID, service, proto, fqdn string, port int) (string, error) {
	return c.createRecord(ctx, zoneID, srvRecord(service, proto, fqdn, port))
}

// EnsureSRVRecord is EnsureHostRecord for the SRV record: an SRV already at the
// name is updated in place, else one is created.
func (c *Client) EnsureSRVRecord(ctx context.Context, zoneID, service, proto, fqdn string, port int) (id string, created bool, err error) {
	rec := srvRecord(service, proto, fqdn, port)
	recs, err := c.FindRecords(ctx, zoneID, rec.Name)
	if err != nil {
		return "", false, err
	}
	for _, r := range recs {
		if r.Type == "SRV" {
			return r.ID, false, c.do(ctx, http.MethodPut, "/zones/"+zoneID+"/dns_records/"+r.ID, rec, nil)
		}
	}
	id, err = c.createRecord(ctx, zoneID, rec)
	return id, err == nil, err
}

// NormalizeService reduces what an operator types as an SRV service to the
// bare label: "_enshrouded._udp", "_Enshrouded" and "enshrouded" all become
// "enshrouded". The field used to suggest "_game._udp", and the protocol was
// then added again, giving "_enshrouded._udp._udp.<name>". "" stays "" (no SRV
// record); anything that isn't a DNS label after that is an error.
func NormalizeService(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	for _, suffix := range []string{"._udp", "._tcp", ".udp", ".tcp"} {
		s = strings.TrimSuffix(s, suffix)
	}
	s = strings.TrimPrefix(s, "_")
	if s == "" {
		return "", nil
	}
	if !serviceLabel.MatchString(s) {
		return "", fmt.Errorf("srv service must be a single name such as \"enshrouded\" (letters, digits and hyphens), got %q", s)
	}
	return s, nil
}

var serviceLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

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
