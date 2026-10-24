package httpx

import (
	"net"
	"net/http"
	"strings"
)

func ClientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
			first := strings.TrimSpace(strings.Split(fwd, ",")[0])
			if ip := normalizeIP(first); ip != "" {
				return ip
			}
		}
		if real := normalizeIP(r.Header.Get("X-Real-IP")); real != "" {
			return real
		}
	}
	if ip := normalizeIP(r.RemoteAddr); ip != "" {
		return ip
	}
	return "unknown"
}

func normalizeIP(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(v); err == nil {
		v = host
	}
	v = strings.Trim(v, "[]")
	if net.ParseIP(v) == nil {
		return ""
	}
	return v
}
