package api

import (
	"net"
	"net/http"
	"strings"

	"github.com/golovanov-dev/alertloop/internal/config"
)

// TrustedProxies decides whether X-Forwarded-For may be believed, and turns a
// request into the client address the per-IP limiter counts against.
//
// Why this exists: the documented production setup puts AlertLoop on loopback
// behind nginx. Every request then arrives from 127.0.0.1, so the per-IP
// limiter sees ONE client and puts the whole internet in a single bucket. Two
// things follow, and both are worse than no limiter at all because the
// documentation says the limiter is there: brute-forcing the admin token is
// unlimited in practice, and any single client can exhaust the shared bucket
// and get everybody else a 429 — a self-inflicted denial of service on an
// alerting product.
//
// The naive fix — always trust X-Forwarded-For — is worse still: the header is
// attacker-controlled, so every request would arrive from a fresh "IP" and the
// limiter would never trigger at all. So the header is believed only when the
// peer that sent it is one we listed.
type TrustedProxies struct {
	nets []*net.IPNet
}

// NewTrustedProxies builds the trusted set from rate_limit.trusted_proxies (see
// config.ParseTrustedProxies). An empty list means no proxy is trusted and
// X-Forwarded-For is ignored entirely, which is the correct default for a
// directly exposed instance.
func NewTrustedProxies(entries []string) (*TrustedProxies, error) {
	nets, err := config.ParseTrustedProxies(entries)
	if err != nil {
		return nil, err
	}
	return &TrustedProxies{nets: nets}, nil
}

// Configured reports whether any proxy is trusted.
func (t *TrustedProxies) Configured() bool { return t != nil && len(t.nets) > 0 }

func (t *TrustedProxies) trusts(ip net.IP) bool {
	if t == nil || ip == nil {
		return false
	}
	for _, n := range t.nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// ClientIP returns the address to rate-limit this request against.
//
// It walks X-Forwarded-For from the RIGHT, which is the end the proxy appends
// to and therefore the only part a client cannot forge. Each hop is accepted
// only while it is itself trusted; the first untrusted address is the real
// client. Reading the header left-to-right instead would let anyone send
// `X-Forwarded-For: 1.2.3.4` and be counted as whoever they liked.
func (t *TrustedProxies) ClientIP(r *http.Request) string {
	peer := hostOnly(r.RemoteAddr)
	if !t.Configured() {
		return peer
	}
	peerIP := net.ParseIP(peer)
	if !t.trusts(peerIP) {
		// Somebody is talking to us directly. Whatever headers they set are
		// their own invention.
		return peer
	}

	forwarded := r.Header.Get("X-Forwarded-For")
	if forwarded == "" {
		// A trusted proxy that forwards no header: fall back to X-Real-IP,
		// which is what the shipped nginx configuration sets.
		if real := strings.TrimSpace(r.Header.Get("X-Real-IP")); real != "" {
			if ip := net.ParseIP(real); ip != nil {
				return ip.String()
			}
		}
		return peer
	}

	parts := strings.Split(forwarded, ",")
	for i := len(parts) - 1; i >= 0; i-- {
		candidate := net.ParseIP(strings.TrimSpace(parts[i]))
		if candidate == nil {
			// Garbage in the chain: stop and use the last thing we believed.
			break
		}
		if !t.trusts(candidate) {
			return candidate.String()
		}
	}
	// Every hop was a trusted proxy. Count against the nearest one.
	return peer
}

// hostOnly returns the IP of a host:port address without its IPv6 zone
// ("fe80::1%eth0" -> "fe80::1"): net.ParseIP rejects a zoned address.
func hostOnly(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if i := strings.IndexByte(host, '%'); i >= 0 {
		host = host[:i]
	}
	return host
}
