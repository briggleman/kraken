// Package unifi is a minimal client for a local UniFi controller's port-forward
// API, authenticated with a UniFi OS API key (X-API-KEY). It implements only what
// the networking feature needs: list/create/enable/delete port forwards and read
// the gateway's WAN IP. UniFi gateways ship with self-signed certs by default;
// operators who have installed a trusted cert can opt into TLS verification.
package unifi

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Client talks to a local UniFi controller with an API key.
type Client struct {
	base string // <url>/proxy/network/api/s/<site>
	key  string
	http *http.Client
}

// New builds a client for the controller at url (e.g. https://192.168.1.1), site
// (default "default" when empty), authenticated by the given API key.
//
// verifyTLS toggles standard TLS verification. Most UniFi gateways serve the
// stock self-signed cert on the LAN, so operators keep this false until they've
// installed a trusted cert on the controller.
func New(url, apiKey, site string, verifyTLS bool) *Client {
	if site == "" {
		site = "default"
	}
	base := strings.TrimRight(url, "/") + "/proxy/network/api/s/" + site
	transport := &http.Transport{}
	if !verifyTLS {
		// Operator-opted-in: skip cert validation for the local self-signed gateway.
		// When verifyTLS is true the default verification path applies.
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12} // #nosec G402 -- operator-selected: LAN self-signed
	}
	return &Client{
		base: base,
		key:  apiKey,
		http: &http.Client{
			Timeout:   15 * time.Second,
			Transport: transport,
		},
	}
}

// PortForward is a UniFi port-forwarding rule (subset of fields we manage).
type PortForward struct {
	ID            string `json:"_id,omitempty"`
	Name          string `json:"name"`
	Enabled       bool   `json:"enabled"`
	Src           string `json:"src"`            // "any"
	DstPort       string `json:"dst_port"`       // external (WAN) port
	Fwd           string `json:"fwd"`            // forward-to LAN IP
	FwdPort       string `json:"fwd_port"`       // forward-to port
	Proto         string `json:"proto"`          // tcp | udp | tcp_udp
	PfwdInterface string `json:"pfwd_interface"` // "wan"
}

// Forward is the input describing a rule to ensure.
type Forward struct {
	ID      string // existing rule id (update) or "" (create)
	Name    string
	Port    int    // both WAN and LAN port (the node listens on this host port)
	LANIP   string // forward target (node LAN IP)
	Proto   string // "tcp" | "udp"
	Enabled bool
}

// do performs a request and decodes the UniFi `{data:[...]}` envelope into out.
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
	req.Header.Set("X-API-KEY", c.key)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("unifi: %s %s: status %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if out == nil {
		return nil
	}
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return fmt.Errorf("unifi: decode %s %s: %w", method, path, err)
	}
	if len(env.Data) == 0 {
		return nil
	}
	return json.Unmarshal(env.Data, out)
}

// ListPortForwards returns all port-forward rules (also used by "Test connection").
func (c *Client) ListPortForwards(ctx context.Context) ([]PortForward, error) {
	var out []PortForward
	if err := c.do(ctx, http.MethodGet, "/rest/portforward", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// EnsureForward creates a rule (when in.ID is empty) or updates an existing one,
// returning the rule id. Used to open a port or re-enable a previously-disabled one.
func (c *Client) EnsureForward(ctx context.Context, in Forward) (string, error) {
	rule := PortForward{
		ID:            in.ID,
		Name:          in.Name,
		Enabled:       in.Enabled,
		Src:           "any",
		DstPort:       strconv.Itoa(in.Port),
		Fwd:           in.LANIP,
		FwdPort:       strconv.Itoa(in.Port),
		Proto:         in.Proto,
		PfwdInterface: "wan",
	}
	var out []PortForward
	if in.ID == "" {
		if err := c.do(ctx, http.MethodPost, "/rest/portforward", rule, &out); err != nil {
			return "", err
		}
	} else {
		if err := c.do(ctx, http.MethodPut, "/rest/portforward/"+in.ID, rule, &out); err != nil {
			return "", err
		}
	}
	if len(out) > 0 && out[0].ID != "" {
		return out[0].ID, nil
	}
	return in.ID, nil
}

// DeleteForward removes a rule. A missing rule is not an error (idempotent cleanup).
func (c *Client) DeleteForward(ctx context.Context, id string) error {
	err := c.do(ctx, http.MethodDelete, "/rest/portforward/"+id, nil, nil)
	if err != nil && strings.Contains(err.Error(), "status 404") {
		return nil
	}
	return err
}

// GatewayWANIP reads the gateway's WAN IP from /stat/health (the "wan" subsystem),
// for the external-IP override. Returns "" with no error when not reported.
func (c *Client) GatewayWANIP(ctx context.Context) (string, error) {
	var health []struct {
		Subsystem string `json:"subsystem"`
		WANIP     string `json:"wan_ip"`
	}
	if err := c.do(ctx, http.MethodGet, "/stat/health", nil, &health); err != nil {
		return "", err
	}
	for _, h := range health {
		if h.Subsystem == "wan" && h.WANIP != "" {
			return h.WANIP, nil
		}
	}
	return "", nil
}
