package memory

import (
	"cmp"
	"context"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/briggleman/kraken/internal/panel/store"
)

// ---- Push-alert devices ----

func (s *Store) UpsertDevice(_ context.Context, d *store.Device) (*store.Device, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := cloneDevice(d)
	next.TokenInvalidAt = nil
	if cur, ok := s.devices[d.ID]; ok && cur.UserID == d.UserID {
		// A refresh: the device's own choices and history stay.
		next.Rules = cloneRules(cur.Rules)
		next.CreatedAt = cur.CreatedAt
		next.LastSentAt = cloneTime(cur.LastSentAt)
	} else {
		// A new device, or another user's install taken over: it starts
		// afresh (see store.DeviceStore).
		next.LastSentAt = nil
	}
	s.devices[d.ID] = next
	return cloneDevice(next), nil
}

func (s *Store) GetDevice(_ context.Context, id string) (*store.Device, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d, ok := s.devices[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return cloneDevice(d), nil
}

func (s *Store) ListDevicesByUser(_ context.Context, userID string) ([]*store.Device, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*store.Device, 0)
	for _, d := range s.devices {
		if d.UserID == userID {
			out = append(out, cloneDevice(d))
		}
	}
	sortDevices(out)
	return out, nil
}

func (s *Store) ListDevices(_ context.Context) ([]*store.Device, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*store.Device, 0, len(s.devices))
	for _, d := range s.devices {
		out = append(out, cloneDevice(d))
	}
	sortDevices(out)
	return out, nil
}

func (s *Store) UpdateDeviceRules(_ context.Context, id string, r store.DeviceRules) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.devices[id]
	if !ok {
		return store.ErrNotFound
	}
	d.Rules = cloneRules(r)
	return nil
}

func (s *Store) DeleteDevice(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.devices[id]; !ok {
		return store.ErrNotFound
	}
	delete(s.devices, id)
	return nil
}

func (s *Store) DeleteDevicesByUser(_ context.Context, userID string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int64
	for id, d := range s.devices {
		if d.UserID == userID {
			delete(s.devices, id)
			n++
		}
	}
	return n, nil
}

func (s *Store) MarkDeviceTokenInvalid(_ context.Context, id, token string, at time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.devices[id]
	if !ok || d.APNsToken != token {
		return false, nil
	}
	if d.TokenInvalidAt == nil {
		d.TokenInvalidAt = &at
	}
	return true, nil
}

func (s *Store) TouchDeviceSent(_ context.Context, id string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.devices[id]
	if !ok {
		return store.ErrNotFound
	}
	d.LastSentAt = &at
	return nil
}

// PanelID generates the install id on first use. Like everything else in this
// store it lives as long as the process does.
func (s *Store) PanelID(_ context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.panelID == "" {
		s.panelID = uuid.NewString()
	}
	return s.panelID, nil
}

// sortDevices orders devices oldest first, as the Postgres store does, with
// the id to break a tie so the order never depends on map iteration.
func sortDevices(ds []*store.Device) {
	slices.SortFunc(ds, func(a, b *store.Device) int {
		if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
}

func cloneDevice(d *store.Device) *store.Device {
	c := *d
	c.PublicKey = slices.Clone(d.PublicKey)
	c.Rules = cloneRules(d.Rules)
	c.LastSentAt = cloneTime(d.LastSentAt)
	c.TokenInvalidAt = cloneTime(d.TokenInvalidAt)
	return &c
}

// cloneRules copies the mute list, and never leaves it nil: Postgres hands
// back the [] it stored, and the API answers with a list either way.
func cloneRules(r store.DeviceRules) store.DeviceRules {
	r.AliveMutedServers = append([]string{}, r.AliveMutedServers...)
	return r
}

func cloneTime(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	v := *t
	return &v
}
