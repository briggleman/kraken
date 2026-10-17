package mail

import (
	"bytes"
	"embed"
	"fmt"
	htmltemplate "html/template"
	texttemplate "text/template"
)

// The message templates, one .txt and one .html per message, rendered as a
// pair so every email carries both parts. They are plain files rather than
// Go strings so the HTML can be opened in a browser while it is worked on.
//
//go:embed templates/*.txt templates/*.html
var templateFS embed.FS

var (
	textTemplates = texttemplate.Must(texttemplate.ParseFS(templateFS, "templates/*.txt"))
	htmlTemplates = htmltemplate.Must(htmltemplate.ParseFS(templateFS, "templates/*.html"))
)

// Render fills the named message template (its .txt and .html files) with
// data and returns the two parts.
func Render(name string, data any) (text, html string, err error) {
	var tb, hb bytes.Buffer
	if err := textTemplates.ExecuteTemplate(&tb, name+".txt", data); err != nil {
		return "", "", fmt.Errorf("mail: render %s.txt: %w", name, err)
	}
	if err := htmlTemplates.ExecuteTemplate(&hb, name+".html", data); err != nil {
		return "", "", fmt.Errorf("mail: render %s.html: %w", name, err)
	}
	return tb.String(), hb.String(), nil
}

// TestData fills the test message the Integrations panel sends.
type TestData struct {
	// PanelHost is the Panel's public host, or "" when no public address is set.
	PanelHost string
	// Username is the admin who pressed the button.
	Username string
	// Relay is host:port of the relay the message went through, and Security
	// how the connection was protected, so the message says which settings
	// it proves.
	Relay    string
	Security string
	SentAt   string
}

// TestSubject is the subject of the test message.
func TestSubject(d TestData) string {
	if d.PanelHost == "" {
		return "Test message from the Kraken panel"
	}
	return "Test message from the Kraken panel at " + d.PanelHost
}
