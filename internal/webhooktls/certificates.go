package webhooktls

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"time"
)

const (
	CACertKey  = "ca.crt"
	CAKeyKey   = "ca.key"
	TLSCertKey = "tls.crt"
	TLSKeyKey  = "tls.key"
)

var errInvalidCertificateData = errors.New("invalid webhook certificate data")

type Bundle struct {
	CAKey   []byte
	CACert  []byte
	TLSKey  []byte
	TLSCert []byte
}

type parsedBundle struct {
	bundle Bundle
	ca     *x509.Certificate
	caKey  crypto.Signer
	leaf   *x509.Certificate
	tls    tls.Certificate
}

type CertificateOptions struct {
	DNSNames         []string
	Now              time.Time
	CAValidity       time.Duration
	ServingValidity  time.Duration
	Backdate         time.Duration
	PreviousCABundle []byte
}

func Generate(options CertificateOptions) (Bundle, error) {
	if len(options.DNSNames) == 0 {
		return Bundle{}, fmt.Errorf("generate webhook certificates: at least one DNS name is required")
	}
	if options.CAValidity <= 0 || options.ServingValidity <= 0 {
		return Bundle{}, fmt.Errorf("generate webhook certificates: validity must be positive")
	}

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return Bundle{}, fmt.Errorf("generate webhook CA key: %w", err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          randomSerial(),
		Subject:               pkix.Name{CommonName: "kontext-webhook-ca"},
		NotBefore:             options.Now.Add(-options.Backdate),
		NotAfter:              options.Now.Add(options.CAValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, caKey.Public(), caKey)
	if err != nil {
		return Bundle{}, fmt.Errorf("create webhook CA certificate: %w", err)
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		return Bundle{}, fmt.Errorf("parse generated webhook CA certificate: %w", err)
	}

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return Bundle{}, fmt.Errorf("generate webhook serving key: %w", err)
	}
	leafTemplate := &x509.Certificate{
		SerialNumber: randomSerial(),
		Subject:      pkix.Name{CommonName: options.DNSNames[0]},
		DNSNames:     append([]string(nil), options.DNSNames...),
		NotBefore:    options.Now.Add(-options.Backdate),
		NotAfter:     options.Now.Add(options.ServingValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, ca, leafKey.Public(), caKey)
	if err != nil {
		return Bundle{}, fmt.Errorf("create webhook serving certificate: %w", err)
	}

	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	if len(options.PreviousCABundle) > 0 {
		caPEM = append(caPEM, options.PreviousCABundle...)
	}
	caKeyDER, err := x509.MarshalPKCS8PrivateKey(caKey)
	if err != nil {
		return Bundle{}, fmt.Errorf("marshal webhook CA key: %w", err)
	}
	leafKeyDER, err := x509.MarshalPKCS8PrivateKey(leafKey)
	if err != nil {
		return Bundle{}, fmt.Errorf("marshal webhook serving key: %w", err)
	}
	return Bundle{
		CAKey:   pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: caKeyDER}),
		CACert:  caPEM,
		TLSKey:  pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: leafKeyDER}),
		TLSCert: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}),
	}, nil
}

func RenewServing(current Bundle, options CertificateOptions) (Bundle, error) {
	parsed, err := parse(current, options.DNSNames, options.Now)
	if err != nil {
		return Bundle{}, err
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return Bundle{}, fmt.Errorf("generate webhook serving key: %w", err)
	}
	template := &x509.Certificate{
		SerialNumber: randomSerial(),
		Subject:      pkix.Name{CommonName: options.DNSNames[0]},
		DNSNames:     append([]string(nil), options.DNSNames...),
		NotBefore:    options.Now.Add(-options.Backdate),
		NotAfter:     options.Now.Add(options.ServingValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, parsed.ca, leafKey.Public(), parsed.caKey)
	if err != nil {
		return Bundle{}, fmt.Errorf("renew webhook serving certificate: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(leafKey)
	if err != nil {
		return Bundle{}, fmt.Errorf("marshal webhook serving key: %w", err)
	}
	return Bundle{
		CAKey:   append([]byte(nil), current.CAKey...),
		CACert:  append([]byte(nil), current.CACert...),
		TLSKey:  pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
		TLSCert: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
	}, nil
}

func parse(bundle Bundle, dnsNames []string, now time.Time) (*parsedBundle, error) {
	caBlock, _ := pem.Decode(bundle.CACert)
	if caBlock == nil || caBlock.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("%w: CA certificate", errInvalidCertificateData)
	}
	ca, err := x509.ParseCertificate(caBlock.Bytes)
	if err != nil || !ca.IsCA {
		return nil, fmt.Errorf("%w: CA certificate", errInvalidCertificateData)
	}
	keyBlock, _ := pem.Decode(bundle.CAKey)
	if keyBlock == nil {
		return nil, fmt.Errorf("%w: CA private key", errInvalidCertificateData)
	}
	keyValue, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%w: CA private key", errInvalidCertificateData)
	}
	caKey, ok := keyValue.(crypto.Signer)
	if !ok || !publicKeysEqual(ca.PublicKey, caKey.Public()) {
		return nil, fmt.Errorf("%w: CA key does not match certificate", errInvalidCertificateData)
	}
	pair, err := tls.X509KeyPair(bundle.TLSCert, bundle.TLSKey)
	if err != nil || len(pair.Certificate) == 0 {
		return nil, fmt.Errorf("%w: serving key pair", errInvalidCertificateData)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("%w: serving certificate", errInvalidCertificateData)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(bundle.CACert) {
		return nil, fmt.Errorf("%w: CA bundle", errInvalidCertificateData)
	}
	for _, dnsName := range dnsNames {
		if _, err := leaf.Verify(x509.VerifyOptions{
			DNSName:     dnsName,
			Roots:       roots,
			CurrentTime: now,
			KeyUsages:   []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		}); err != nil {
			return nil, fmt.Errorf("%w: serving certificate verification", errInvalidCertificateData)
		}
	}
	pair.Leaf = leaf
	return &parsedBundle{bundle: bundle, ca: ca, caKey: caKey, leaf: leaf, tls: pair}, nil
}

func publicKeysEqual(left, right crypto.PublicKey) bool {
	leftDER, leftErr := x509.MarshalPKIXPublicKey(left)
	rightDER, rightErr := x509.MarshalPKIXPublicKey(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftDER, rightDER)
}

func randomSerial() *big.Int {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, limit)
	if err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return serial
}
