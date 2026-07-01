package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/briggleman/kraken/internal/shared/mtls"
)

// enroll generates an Agent key + CSR, exchanges a one-time bootstrap token with
// the Panel for a signed certificate, and writes the mTLS material to disk.
func enroll(args []string) error {
	var panelURL, token, hosts string
	out := "./certs"
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-panel":
			i++
			if i < len(args) {
				panelURL = args[i]
			}
		case "-token":
			i++
			if i < len(args) {
				token = args[i]
			}
		case "-hosts":
			i++
			if i < len(args) {
				hosts = args[i]
			}
		case "-out":
			i++
			if i < len(args) {
				out = args[i]
			}
		default:
			return fmt.Errorf("unknown flag %q", args[i])
		}
	}
	if panelURL == "" || token == "" {
		return fmt.Errorf("-panel and -token are required")
	}

	var hostList []string
	for _, h := range strings.Split(hosts, ",") {
		if h = strings.TrimSpace(h); h != "" {
			hostList = append(hostList, h)
		}
	}

	keyPEM, csrPEM, err := mtls.NewAgentKeyAndCSR(hostList)
	if err != nil {
		return fmt.Errorf("generate key/CSR: %w", err)
	}

	reqBody, _ := json.Marshal(map[string]string{"token": token, "csr": string(csrPEM)})
	url := strings.TrimRight(panelURL, "/") + "/api/v1/agents/enroll"
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Post(url, "application/json", bytes.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("contact panel: %w", err)
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("enroll failed (%s): %s", resp.Status, strings.TrimSpace(string(rb)))
	}
	var er struct {
		Certificate string `json:"certificate"`
		CA          string `json:"ca"`
	}
	if err := json.Unmarshal(rb, &er); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	if er.Certificate == "" || er.CA == "" {
		return fmt.Errorf("enroll response missing certificate/ca")
	}

	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, "agent-key.pem"), keyPEM, 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, "agent.pem"), []byte(er.Certificate), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, "ca.pem"), []byte(er.CA), 0o644); err != nil {
		return err
	}

	fmt.Printf("Enrolled. Wrote agent.pem, agent-key.pem, ca.pem to %s\n", out)
	fmt.Println("Agent:  KRAKEN_TLS_CERT=agent.pem KRAKEN_TLS_KEY=agent-key.pem KRAKEN_TLS_CA=ca.pem")
	return nil
}
