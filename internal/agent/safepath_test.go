package agent

import "testing"

// TestSafePath covers the data-root jail, including the Windows mapping of the
// logical "/data" root (used by clients regardless of node OS) onto "C:/data".
func TestSafePath(t *testing.T) {
	win := &DockerRuntime{osType: "windows"}
	lin := &DockerRuntime{osType: "linux"}

	ok := []struct {
		rt   *DockerRuntime
		in   string
		want string
	}{
		{win, "/data", "C:/data"},                 // logical root from the UI
		{win, "/data/save/x", "C:/data/save/x"},   // logical subpath
		{win, `C:\data\save`, "C:/data/save"},     // native Windows path
		{win, "C:/data", "C:/data"},               // already-rooted
		{win, "", "C:/data"},                      // empty → root
		{win, "save/world", "C:/data/save/world"}, // relative
		{lin, "/data", "/data"},
		{lin, "/data/x", "/data/x"},
		{lin, "", "/data"},
	}
	for _, c := range ok {
		got, err := c.rt.safePath(c.in)
		if err != nil {
			t.Errorf("safePath(%q) [%s]: unexpected error %v", c.in, c.rt.osType, err)
			continue
		}
		if got != c.want {
			t.Errorf("safePath(%q) [%s] = %q, want %q", c.in, c.rt.osType, got, c.want)
		}
	}

	escapes := []struct {
		rt *DockerRuntime
		in string
	}{
		{win, "/etc/passwd"},
		{win, "/data/../secrets"},
		{lin, "/etc/passwd"},
		{lin, "/data/../secrets"},
	}
	for _, c := range escapes {
		if _, err := c.rt.safePath(c.in); err == nil {
			t.Errorf("safePath(%q) [%s]: expected escape error, got nil", c.in, c.rt.osType)
		}
	}
}
