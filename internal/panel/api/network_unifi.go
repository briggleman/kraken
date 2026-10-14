package api

import (
	"context"
	"fmt"
	"time"

	"github.com/briggleman/kraken/internal/panel/store"
	"github.com/briggleman/kraken/internal/panel/unifi"
)

// syncTimeout bounds one address change's UniFi work: a reservation and a
// forward per open port, each a round trip to the controller.
const syncTimeout = 60 * time.Second

// serverAddressHook, when set, replaces the background sync so tests can watch
// it run. Nil in the Panel.
var serverAddressHook func(serverID string)

// serverAddressChanged is called by the reconciler when a server's container
// address changed: first leased on a transparent network, or gone because the
// server is back behind NAT. It re-points everything that names that address
// in the background, off the status poll's path.
func (s *Server) serverAddressChanged(serverID string) {
	if serverAddressHook != nil {
		serverAddressHook(serverID)
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), syncTimeout)
		defer cancel()
		if err := s.syncServerAddress(ctx, serverID); err != nil {
			s.logger.Warn("could not update the server's network bindings after its address changed", "server", serverID, "err", err)
		}
	}()
}

// syncServerAddress brings a server's UniFi state in line with its current
// address (#426):
//   - with a container address, the server's MAC is pinned to it, so DHCP hands
//     the same address back after every restart;
//   - without one, any pin the Panel made is released;
//   - either way, each open port forward is re-aimed at serverLANHost.
//
// UniFi not configured is not an error: the server still gets an address by
// DHCP, it just isn't reserved and nothing is forwarded to it.
func (s *Server) syncServerAddress(ctx context.Context, serverID string) error {
	uc := s.unifiClient(ctx)
	if uc == nil {
		return nil
	}
	sv, err := s.store.GetServer(ctx, serverID)
	if err != nil {
		return err
	}
	node, err := s.store.GetNode(ctx, sv.NodeID)
	if err != nil {
		return err
	}

	var res *store.ServerReservation
	switch {
	case sv.ContainerIP != "":
		mac := serverMAC(sv.ID)
		if sv.Reservation == nil || sv.Reservation.IP != sv.ContainerIP || sv.Reservation.MAC != mac {
			net, err := uc.NetworkFor(ctx, sv.ContainerIP)
			if err != nil {
				return err
			}
			if err := uc.EnsureReservation(ctx, unifi.Reservation{
				MAC:       mac,
				Name:      "kraken " + sv.Name,
				Note:      "Kraken game server " + sv.ID + ". Managed by the Panel; removed when the server is deleted.",
				IP:        sv.ContainerIP,
				NetworkID: net.ID,
			}); err != nil {
				return fmt.Errorf("reserve %s for %s: %w", sv.ContainerIP, mac, err)
			}
		}
		res = &store.ServerReservation{MAC: mac, IP: sv.ContainerIP}
	case sv.Reservation != nil:
		if err := uc.ForgetClient(ctx, sv.Reservation.MAC); err != nil {
			return fmt.Errorf("release the reservation for %s: %w", sv.Reservation.MAC, err)
		}
	}

	target := serverLANHost(sv, node)
	sp, err := s.store.GetSpec(ctx, sv.SpecID)
	if err != nil {
		return err
	}
	for portName, f := range sv.Forwards {
		port := sv.Ports[portName]
		if port == 0 || !f.Enabled {
			continue
		}
		if _, err := uc.EnsureForward(ctx, unifi.Forward{
			ID:      f.RuleID,
			Name:    fmt.Sprintf("kraken-%s-%s", sv.Name, portName),
			Port:    port,
			LANIP:   target,
			Proto:   protoForPort(sp, portName),
			Enabled: true,
		}); err != nil {
			return fmt.Errorf("re-aim the %s forward at %s: %w", portName, target, err)
		}
	}

	// Write onto a fresh read: the reconciler writes this row on every poll.
	fresh, err := s.store.GetServer(ctx, serverID)
	if err != nil {
		return err
	}
	fresh.Reservation = res
	return s.store.UpdateServer(ctx, fresh)
}
