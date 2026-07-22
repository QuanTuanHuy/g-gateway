package proxy

import (
	"crypto/tls"
	"net"
	"net/http"
	"strings"
)

var hopByHopHeaders = []string{
	"Connection",
	"Proxy-Connection",
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"TE",
	"Trailer",
	"Transfer-Encoding",
	"Upgrade",
}

var forwardingHeaders = []string{
	"Forwarded",
	"X-Forwarded-For",
	"X-Forwarded-Host",
	"X-Forwarded-Proto",
	"X-Forwarded-Port",
}

func removeHopByHopHeaders(header http.Header) {
	for _, connection := range header.Values("Connection") {
		for token := range strings.SplitSeq(connection, ",") {
			header.Del(strings.TrimSpace(token))
		}
	}
	for _, name := range hopByHopHeaders {
		header.Del(name)
	}
}

func rebuildForwardingHeaders(outbound http.Header, inbound *http.Request) {
	for _, name := range forwardingHeaders {
		outbound.Del(name)
	}

	clientIP := inbound.RemoteAddr
	if host, _, err := net.SplitHostPort(inbound.RemoteAddr); err == nil {
		clientIP = host
	}
	if clientIP != "" {
		outbound.Set("X-Forwarded-For", clientIP)
	}
	outbound.Set("X-Forwarded-Host", inbound.Host)
	if inbound.TLS == nil {
		outbound.Set("X-Forwarded-Proto", "http")
	} else {
		outbound.Set("X-Forwarded-Proto", "https")
	}
	outbound.Set("X-Forwarded-Port", requestPort(inbound.Host, inbound.TLS))
}

func requestPort(host string, tlsState *tls.ConnectionState) string {
	if _, port, err := net.SplitHostPort(host); err == nil {
		return port
	}
	if tlsState != nil {
		return "443"
	}
	return "80"
}
