package webhooktls

import (
	"crypto/tls"
	"fmt"
	"sync"
)

type Store struct {
	mu       sync.RWMutex
	cert     *tls.Certificate
	caBundle []byte
}

func (s *Store) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.cert == nil {
		return nil, fmt.Errorf("webhook serving certificate is not initialized")
	}
	return s.cert, nil
}

func (s *Store) load(parsed *parsedBundle) {
	s.mu.Lock()
	defer s.mu.Unlock()
	certificate := parsed.tls
	s.cert = &certificate
	s.caBundle = append([]byte(nil), parsed.bundle.CACert...)
}

func (s *Store) CABundle() []byte {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]byte(nil), s.caBundle...)
}
