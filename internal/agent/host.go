package agent

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// PrimaryIP returns the host's primary non-loopback IPv4 address — the address
// other machines would use to reach this node. It uses a UDP "dial" (no packets
// are sent) to learn which local interface routes outbound traffic, falling back
// to interface enumeration. Returns "" if none can be determined.
func PrimaryIP() string {
	if conn, err := net.Dial("udp", "8.8.8.8:80"); err == nil {
		defer conn.Close()
		if ua, ok := conn.LocalAddr().(*net.UDPAddr); ok && ua.IP.To4() != nil && !ua.IP.IsLoopback() {
			return ua.IP.String()
		}
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		if ipnet, ok := a.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if v4 := ipnet.IP.To4(); v4 != nil {
				return v4.String()
			}
		}
	}
	return ""
}

// externalIPTTL is how long a fetched WAN IP is cached before re-querying.
const externalIPTTL = 10 * time.Minute

var (
	extIPMu     sync.Mutex
	extIPValue  string
	extIPExpiry time.Time
)

// externalIPProbes are outbound "what's my IP" endpoints, tried in order. The IP
// the request egresses from is the node's outward-facing (WAN) address.
var externalIPProbes = []struct {
	url   string
	parse func(string) string
}{
	{"https://cloudflare.com/cdn-cgi/trace", parseCloudflareTrace},
	{"https://api.ipify.org", strings.TrimSpace},
}

// ExternalIP returns the node's outward-facing IP by querying an external echo
// service, cached for externalIPTTL. Best-effort: returns "" if unreachable
// (e.g. an isolated LAN). Cross-OS, pure net/http.
func ExternalIP(ctx context.Context) string {
	extIPMu.Lock()
	if extIPValue != "" && time.Now().Before(extIPExpiry) {
		v := extIPValue
		extIPMu.Unlock()
		return v
	}
	extIPMu.Unlock()

	client := &http.Client{Timeout: 3 * time.Second}
	for _, p := range externalIPProbes {
		cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		req, err := http.NewRequestWithContext(cctx, http.MethodGet, p.url, nil)
		if err != nil {
			cancel()
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			cancel()
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		cancel()
		if resp.StatusCode/100 != 2 {
			continue
		}
		if ip := net.ParseIP(strings.TrimSpace(p.parse(string(body)))); ip != nil {
			extIPMu.Lock()
			extIPValue, extIPExpiry = ip.String(), time.Now().Add(externalIPTTL)
			extIPMu.Unlock()
			return ip.String()
		}
	}
	return ""
}

// parseCloudflareTrace extracts the ip= line from a cdn-cgi/trace body.
func parseCloudflareTrace(body string) string {
	for _, line := range strings.Split(body, "\n") {
		if v, ok := strings.CutPrefix(line, "ip="); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
