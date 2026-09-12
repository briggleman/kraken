package agent

import "testing"

// The watchdog's crash line is what a journal-reading operator has, and a
// Windows NTSTATUS is unrecognisable in decimal: 3221225781 means nothing,
// 0xC0000135 is STATUS_DLL_NOT_FOUND. The uint32 conversion is the load-bearing
// part — a signed render would print -1073741515 and match no documentation
// anywhere.
func TestHexExit(t *testing.T) {
	for _, tc := range []struct {
		name string
		code int64
		want string
	}{
		{"missing dll", 3221225781, "0xC0000135"},
		{"entry point not found", 3221225785, "0xC0000139"},
		{"access violation", 3221225477, "0xC0000005"},
		{"bad image format", 3221225595, "0xC000007B"},
		{"clean exit", 0, "0x00000000"},
		{"linux sigkill", 137, "0x00000089"},
	} {
		if got := hexExit(tc.code); got != tc.want {
			t.Errorf("%s: hexExit(%d) = %s, want %s", tc.name, tc.code, got, tc.want)
		}
	}
}
