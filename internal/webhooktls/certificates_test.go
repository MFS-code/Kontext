package webhooktls

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"testing"
	"time"
)

func TestGenerateCertificateConstraints(t *testing.T) {
	now := time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC)
	dnsNames := []string{"webhook-service", "webhook-service.kontext-system.svc"}
	bundle, err := Generate(CertificateOptions{
		DNSNames:        dnsNames,
		Now:             now,
		CAValidity:      24 * time.Hour,
		ServingValidity: 12 * time.Hour,
		Backdate:        5 * time.Minute,
	})
	if err != nil {
		t.Fatalf("generate certificates: %v", err)
	}
	parsed, err := parse(bundle, dnsNames, now)
	if err != nil {
		t.Fatalf("parse generated certificates: %v", err)
	}
	if !parsed.ca.IsCA || parsed.ca.KeyUsage&x509.KeyUsageCertSign == 0 {
		t.Fatalf("CA constraints are invalid: %#v", parsed.ca)
	}
	if parsed.leaf.KeyUsage&x509.KeyUsageDigitalSignature == 0 ||
		len(parsed.leaf.ExtKeyUsage) != 1 ||
		parsed.leaf.ExtKeyUsage[0] != x509.ExtKeyUsageServerAuth {
		t.Fatalf("serving key usage is invalid: %#v", parsed.leaf)
	}
	for _, dnsName := range dnsNames {
		if err := parsed.leaf.VerifyHostname(dnsName); err != nil {
			t.Fatalf("serving certificate does not cover %q: %v", dnsName, err)
		}
	}
}

func TestRenewServingPreservesCAAndReplacesLeaf(t *testing.T) {
	now := time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC)
	options := CertificateOptions{
		DNSNames:        []string{"webhook-service.kontext-system.svc"},
		Now:             now,
		CAValidity:      48 * time.Hour,
		ServingValidity: 2 * time.Hour,
		Backdate:        time.Minute,
	}
	original, err := Generate(options)
	if err != nil {
		t.Fatalf("generate certificates: %v", err)
	}
	options.Now = now.Add(time.Hour)
	renewed, err := RenewServing(original, options)
	if err != nil {
		t.Fatalf("renew serving certificate: %v", err)
	}
	if !bytes.Equal(original.CACert, renewed.CACert) || !bytes.Equal(original.CAKey, renewed.CAKey) {
		t.Fatal("serving renewal replaced CA material")
	}
	if bytes.Equal(original.TLSCert, renewed.TLSCert) || bytes.Equal(original.TLSKey, renewed.TLSKey) {
		t.Fatal("serving renewal reused leaf material")
	}
	if _, err := parse(renewed, options.DNSNames, options.Now); err != nil {
		t.Fatalf("renewed certificate is invalid: %v", err)
	}
}

func TestParseRejectsInvalidOrSkewedCertificateData(t *testing.T) {
	now := time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC)
	options := CertificateOptions{
		DNSNames:        []string{"webhook-service.kontext-system.svc"},
		Now:             now,
		CAValidity:      time.Hour,
		ServingValidity: time.Hour,
		Backdate:        time.Minute,
	}
	bundle, err := Generate(options)
	if err != nil {
		t.Fatalf("generate certificates: %v", err)
	}
	bundle.TLSKey = []byte("not a private key")
	if _, err := parse(bundle, options.DNSNames, now); !errors.Is(err, errInvalidCertificateData) {
		t.Fatalf("expected invalid certificate error, got %v", err)
	}

	valid, err := Generate(options)
	if err != nil {
		t.Fatalf("generate certificates: %v", err)
	}
	if _, err := parse(valid, options.DNSNames, now.Add(2*time.Hour)); !errors.Is(err, errInvalidCertificateData) {
		t.Fatalf("expected expired certificate error, got %v", err)
	}
}

func TestGenerateCarriesPreviousCAForSafeRotation(t *testing.T) {
	now := time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC)
	first, err := Generate(CertificateOptions{
		DNSNames:        []string{"webhook-service"},
		Now:             now,
		CAValidity:      24 * time.Hour,
		ServingValidity: time.Hour,
		Backdate:        time.Minute,
	})
	if err != nil {
		t.Fatalf("generate first certificates: %v", err)
	}
	second, err := Generate(CertificateOptions{
		DNSNames:         []string{"webhook-service"},
		Now:              now,
		CAValidity:       48 * time.Hour,
		ServingValidity:  time.Hour,
		Backdate:         time.Minute,
		PreviousCABundle: first.CACert,
	})
	if err != nil {
		t.Fatalf("generate rotating certificates: %v", err)
	}
	if !bytes.Contains(second.CACert, first.CACert) {
		t.Fatal("rotated CA bundle does not preserve previous trust")
	}
	var certificates int
	rest := second.CACert
	for {
		block, remaining := pem.Decode(rest)
		if block == nil {
			break
		}
		certificates++
		rest = remaining
	}
	if certificates != 2 {
		t.Fatalf("rotated CA bundle contains %d certificates, want 2", certificates)
	}
}
