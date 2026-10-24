package httpx

import (
	"net"
	"net/http"
	"strings"
)

func ClientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		// Use the rightmost hop. The left entry is client-supplied and
		// spoofable; each proxy appends the address it actually saw.
		if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
			parts := strings.Split(fwd, ",")
			for i := len(parts) - 1; i >= 0; i-- {
				if ip := normalizeIP(parts[i]); ip != "" {
					return ip
				}
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
