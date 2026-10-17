package mail

import (
	"errors"
	"fmt"
	"net/smtp"
	"strings"
)

// loginAuth is the AUTH LOGIN mechanism, which net/smtp does not ship:
// username and password sent base64 in turn, each in answer to a prompt.
// Microsoft 365 and a number of appliances offer only this. It keeps
// net/smtp's PLAIN rule: never over an unencrypted connection, unless the
// relay is this very host.
type loginAuth struct {
	username, password, host string
}

func (a *loginAuth) Start(server *smtp.ServerInfo) (string, []byte, error) {
	if !server.TLS && !isLocalhost(server.Name) {
		return "", nil, errors.New("refusing to send the password over an unencrypted connection; use starttls or tls")
	}
	if server.Name != a.host {
		return "", nil, errors.New("wrong host name")
	}
	return "LOGIN", nil, nil
}

func (a *loginAuth) Next(fromServer []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}
	switch strings.ToLower(strings.TrimSpace(string(fromServer))) {
	case "username:", "username":
		return []byte(a.username), nil
	case "password:", "password":
		return []byte(a.password), nil
	}
	return nil, fmt.Errorf("unexpected LOGIN prompt %q", fromServer)
}

func isLocalhost(name string) bool {
	return name == "localhost" || name == "127.0.0.1" || name == "::1"
}
