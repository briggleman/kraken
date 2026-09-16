package api

import "time"

// DownloadRedeemBurstForTest is the download limiter's burst, so a test in the
// black-box package can walk up to the edge of it without restating the number.
const DownloadRedeemBurstForTest = downloadRedeemBurst

// ExpireDownloadTokensForTest backdates every outstanding file-download token
// so a test can exercise the expiry branch without waiting out the 60-second
// TTL. It lives in a _test.go file, so it is compiled only into the test
// binary and is not part of the Panel.
func (s *Server) ExpireDownloadTokensForTest() {
	s.downloads.mu.Lock()
	defer s.downloads.mu.Unlock()
	for k, g := range s.downloads.grants {
		g.expires = time.Now().Add(-time.Second)
		s.downloads.grants[k] = g
	}
}
