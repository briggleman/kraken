//go:build !linux && !windows

package agent

import "time"

// hostReader on platforms the Agent doesn't ship builds for (darwin, mostly, for
// a developer running the test suite). Every metric reports unknown, which the
// node band already renders as "no data" — the same path a Windows host takes
// for temperature. This exists so the package compiles everywhere, not to be a
// real collector.
type hostReader struct {
	dataDir string
}

func newHostReader(dataDir string) *hostReader { return &hostReader{dataDir: dataDir} }

func (r *hostReader) read() hostSnapshot {
	return hostSnapshot{at: time.Now(), diskPath: r.dataDir}
}
