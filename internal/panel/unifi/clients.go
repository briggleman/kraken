package unifi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
)

// DHCP reservations for game containers on a transparent network (#426). Each
// container keeps one MAC for the life of its server, and the Panel pins that
// MAC to the address DHCP first leased it, so the server comes back on the same
// address after every restart and its port forward stays valid.

// Network is a UniFi network (rest/networkconf), the subset needed to find
// which one an address belongs to.
type Network struct {
	ID     string
	Name   string
	Subnet string // gateway CIDR as UniFi stores it, e.g. "192.168.20.1/24"
	VLAN   int    // 0 when untagged
}

func (n *Network) UnmarshalJSON(b []byte) error {
	var raw struct {
		ID     string          `json:"_id"`
		Name   string          `json:"name"`
		Subnet string          `json:"ip_subnet"`
		VLAN   json.RawMessage `json:"vlan"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	n.ID, n.Name, n.Subnet = raw.ID, raw.Name, raw.Subnet
	// Older controllers store the VLAN id as a string, newer ones as a number.
	v := strings.Trim(string(raw.VLAN), `"`)
	if v != "" && v != "null" {
		if id, err := strconv.Atoi(v); err == nil {
			n.VLAN = id
		}
	}
	return nil
}

// Contains reports whether ip falls in the network's subnet.
func (n Network) Contains(ip string) bool {
	p, err := netip.ParsePrefix(n.Subnet)
	if err != nil {
		return false
	}
	a, err := netip.ParseAddr(ip)
	return err == nil && p.Masked().Contains(a)
}

// NetworkFor returns the network whose subnet holds ip. The reservation has to
// name the network the client is on, and the address is the reliable way to
// tell which one that is: the VLAN a container tags may be untagged at the
// switch, or the operator may run two networks on one VLAN id.
func (c *Client) NetworkFor(ctx context.Context, ip string) (Network, error) {
	var nets []Network
	if err := c.do(ctx, http.MethodGet, "/rest/networkconf", nil, &nets); err != nil {
		return Network{}, err
	}
	for _, n := range nets {
		if n.Contains(ip) {
			return n, nil
		}
	}
	return Network{}, fmt.Errorf("unifi: no network holds %s", ip)
}

// Reservation is a fixed-IP client entry (rest/user).
type Reservation struct {
	MAC       string
	Name      string
	Note      string
	IP        string
	NetworkID string
}

type userEntry struct {
	ID         string `json:"_id,omitempty"`
	MAC        string `json:"mac,omitempty"`
	Name       string `json:"name,omitempty"`
	Note       string `json:"note,omitempty"`
	UseFixedIP bool   `json:"use_fixedip"`
	FixedIP    string `json:"fixed_ip,omitempty"`
	NetworkID  string `json:"network_id,omitempty"`
}

// EnsureReservation pins in.MAC to in.IP on in.NetworkID, creating the client
// entry or updating the one the controller already keeps for that MAC — it
// records every client it has seen, so a container that has leased once is
// usually there already.
func (c *Client) EnsureReservation(ctx context.Context, in Reservation) error {
	mac := strings.ToLower(in.MAC)
	var users []userEntry
	if err := c.do(ctx, http.MethodGet, "/rest/user", nil, &users); err != nil {
		return err
	}
	entry := userEntry{
		MAC: mac, Name: in.Name, Note: in.Note,
		UseFixedIP: true, FixedIP: in.IP, NetworkID: in.NetworkID,
	}
	for _, u := range users {
		if strings.EqualFold(u.MAC, mac) {
			if u.UseFixedIP && u.FixedIP == in.IP && u.NetworkID == in.NetworkID {
				return nil
			}
			return c.do(ctx, http.MethodPut, "/rest/user/"+u.ID, entry, nil)
		}
	}
	return c.do(ctx, http.MethodPost, "/rest/user", entry, nil)
}

// ForgetClient removes the controller's record of a MAC, its fixed IP with it.
// A MAC the controller doesn't know is not an error.
func (c *Client) ForgetClient(ctx context.Context, mac string) error {
	body := map[string]any{"cmd": "forget-sta", "macs": []string{strings.ToLower(mac)}}
	err := c.do(ctx, http.MethodPost, "/cmd/stamgr", body, nil)
	if err != nil && strings.Contains(err.Error(), "status 404") {
		return nil
	}
	return err
}
