package mcpclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	maxHTTPWireBytes        = int64(64 << 20)
	httpCloseRequestTimeout = 2 * time.Second
)

var errMCPHTTPWireLimit = errors.New("mcp_http_wire_limit_exceeded")

type boundedHTTPTransport struct {
	base           http.RoundTripper
	endpointOrigin string
	headers        map[string]string
	maxWireBytes   int64
}

func (transport *boundedHTTPTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	cloned := request.Clone(request.Context())
	cloned.Header = request.Header.Clone()
	if origin(cloned.URL) == transport.endpointOrigin {
		for name, value := range transport.headers {
			cloned.Header.Set(name, value)
		}
	} else {
		for name := range transport.headers {
			cloned.Header.Del(name)
		}
	}
	var cancel context.CancelFunc
	if cloned.Method == http.MethodDelete {
		closeCtx, closeCancel := context.WithTimeout(cloned.Context(), httpCloseRequestTimeout)
		cancel = closeCancel
		cloned = cloned.Clone(closeCtx)
	}
	response, err := transport.base.RoundTrip(cloned)
	if err != nil {
		if cancel != nil {
			cancel()
		}
		return nil, err
	}
	wireLimit := transport.maxWireBytes
	if wireLimit <= 0 {
		wireLimit = maxHTTPWireBytes
	}
	if response.ContentLength > wireLimit {
		_ = response.Body.Close()
		if cancel != nil {
			cancel()
		}
		return nil, fmt.Errorf(
			"%w: MCP HTTP response Content-Length exceeds %d-byte wire limit",
			errMCPHTTPWireLimit,
			wireLimit,
		)
	}
	response.Body = &countingReadCloser{
		body:   response.Body,
		limit:  wireLimit,
		cancel: cancel,
	}
	return response, nil
}

type countingReadCloser struct {
	body      io.ReadCloser
	limit     int64
	read      int64
	exceeded  bool
	cancel    context.CancelFunc
	closeOnce sync.Once
	closeErr  error
}

func (reader *countingReadCloser) Read(buffer []byte) (int, error) {
	if reader.exceeded {
		return 0, errMCPHTTPWireLimit
	}
	if len(buffer) == 0 {
		return 0, nil
	}
	remaining := reader.limit - reader.read
	maximumRead := int64(len(buffer))
	if maximumRead > remaining+1 {
		maximumRead = remaining + 1
	}
	if maximumRead < 1 {
		maximumRead = 1
	}
	count, err := reader.body.Read(buffer[:maximumRead])
	if int64(count) > remaining {
		allowed := int(max(remaining, 0))
		reader.read += int64(allowed)
		reader.exceeded = true
		return allowed, errMCPHTTPWireLimit
	}
	reader.read += int64(count)
	return count, err
}

func (reader *countingReadCloser) Close() error {
	reader.closeOnce.Do(func() {
		reader.closeErr = reader.body.Close()
		if reader.cancel != nil {
			reader.cancel()
		}
	})
	return reader.closeErr
}

func sameOriginRedirectPolicy(endpoint *url.URL) func(*http.Request, []*http.Request) error {
	expectedOrigin := origin(endpoint)
	return func(request *http.Request, via []*http.Request) error {
		if origin(request.URL) != expectedOrigin {
			return errors.New("MCP cross-origin redirect is not allowed")
		}
		if len(via) >= 10 {
			return errors.New("MCP redirect limit exceeded")
		}
		return nil
	}
}

func origin(value *url.URL) string {
	if value == nil {
		return ""
	}
	scheme := strings.ToLower(value.Scheme)
	port := value.Port()
	if port == "" {
		switch scheme {
		case "http":
			port = "80"
		case "https":
			port = "443"
		}
	}
	host := strings.ToLower(value.Hostname())
	if port == "" {
		return scheme + "://" + host
	}
	return scheme + "://" + net.JoinHostPort(host, port)
}
