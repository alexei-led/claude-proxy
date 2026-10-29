package relay

import (
	"crypto/tls"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"
)

func newTransport() *http.Transport {
	return &http.Transport{
		Proxy:                  nil,
		DialContext:            (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:    10 * time.Second,
		ResponseHeaderTimeout:  180 * time.Second,
		ExpectContinueTimeout:  time.Second,
		IdleConnTimeout:        90 * time.Second,
		MaxIdleConns:           16,
		MaxIdleConnsPerHost:    maxInflight,
		MaxConnsPerHost:        maxInflight,
		MaxResponseHeaderBytes: maxHeaderBytes,
		DisableCompression:     true,
		ForceAttemptHTTP2:      true,
	}
}

func (s *Server) proxy(route *upstream, target *url.URL) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Transport:     s.transport,
		FlushInterval: -1,
		ErrorLog:      log.New(io.Discard, "", 0),
		Rewrite: func(pr *httputil.ProxyRequest) {
			// Do not SetURL: its path joining and query cleanup change this contract.
			destination := *target
			destination.RawQuery = pr.In.URL.RawQuery
			destination.ForceQuery = pr.In.URL.ForceQuery
			pr.Out.URL = &destination
			pr.Out.Host = target.Host
			oauth, _ := oauthAuthorization(pr.In.Header)
			// Rewrite runs after hop-by-hop cleanup. Never reconstruct arbitrary
			// client headers on non-OAuth routes.
			pr.Out.Header = outboundHeaders(pr.Out.Header, route, oauth)
			pr.Out.GetBody = nil
			pr.Out.Trailer = nil
		},
		ModifyResponse: func(response *http.Response) error {
			if response.StatusCode == http.StatusSwitchingProtocols {
				return errors.New("upstream protocol upgrade rejected")
			}
			stripCredentials(response.Header)
			stripCredentials(response.Trailer)
			response.Body = &cleanTrailers{ReadCloser: response.Body, response: response}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			status := http.StatusBadGateway
			var networkError net.Error
			if errors.As(err, &networkError) && networkError.Timeout() {
				status = http.StatusGatewayTimeout
			}
			localError(w, status)
		},
	}
}

// Transport populates trailers at EOF, after ModifyResponse has run.
type cleanTrailers struct {
	io.ReadCloser
	response *http.Response
}

func (body *cleanTrailers) Read(buffer []byte) (int, error) {
	n, err := body.ReadCloser.Read(buffer)
	if err != nil {
		stripCredentials(body.response.Trailer)
	}
	return n, err
}
