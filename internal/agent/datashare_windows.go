//go:build windows

package agent

// shareDataTree is a no-op on a Windows host: there is no POSIX group to share
// the data dir through, and agentGroupID never offers one here.
func shareDataTree(string, int, int) error { return nil }
