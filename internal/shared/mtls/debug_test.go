package mtls

import (
	"strings"
	"testing"
)

func TestCAFingerprintPinning(t *testing.T) {
	caPEM, _, err := GenerateCA()
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}
	fp, err := CAFingerprintPEM(caPEM)
	if err != nil {
		t.Fatalf("CAFingerprintPEM: %v", err)
	}
	if len(fp) != 64 {
		t.Fatalf("fingerprint length = %d, want 64 hex chars", len(fp))
	}

	// Exact, uppercase, and sha256:-prefixed spellings all verify.
	for _, pin := range []string{fp, strings.ToUpper(fp), "sha256:" + fp, "  " + fp + "\n"} {
		if err := VerifyCAFingerprint(caPEM, pin); err != nil {
			t.Errorf("VerifyCAFingerprint(%q) = %v, want nil", pin, err)
		}
	}

	// A different CA must not verify.
	otherPEM, _, err := GenerateCA()
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}
	if err := VerifyCAFingerprint(otherPEM, fp); err == nil {
		t.Error("VerifyCAFingerprint accepted a mismatched CA")
	}
	if err := VerifyCAFingerprint(caPEM, ""); err == nil {
		t.Error("VerifyCAFingerprint accepted an empty pin")
	}
	if _, err := CAFingerprintPEM([]byte("not pem")); err == nil {
		t.Error("CAFingerprintPEM accepted garbage input")
	}
}
