package api

import "net/http"

// appleAppSiteAssociationPath is where Apple looks for a domain's
// apple-app-site-association file. Apple's CDN fetches it anonymously, over
// HTTPS, from the domain's root, and does not follow redirects.
const appleAppSiteAssociationPath = "/.well-known/apple-app-site-association"

// appleAppSiteAssociation is the file's JSON body. Only the webcredentials
// service is declared: it lets iCloud Keychain and password managers offer
// this Panel's saved sign-in inside the listed apps. Universal links and
// app clips are not claimed.
type appleAppSiteAssociation struct {
	WebCredentials appleAppList `json:"webcredentials"`
}

type appleAppList struct {
	Apps []string `json:"apps"`
}

// handleAppleAppSiteAssociation serves the apple-app-site-association file
// for KRAKEN_APPLE_APP_IDS. It is unauthenticated on purpose, since Apple
// carries no credentials, and it discloses nothing beyond the app IDs the
// operator chose to publish. With no app IDs configured it is a plain 404,
// never the web UI's index page: a path Apple reads as JSON must not answer
// 200 with HTML.
func (s *Server) handleAppleAppSiteAssociation(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.AppleAppSiteAssociationEnabled() {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, appleAppSiteAssociation{
		WebCredentials: appleAppList{Apps: s.cfg.AppleAppIDs},
	})
}
