package urlcheck

import (
	"net"
	"net/url"
	"strings"
)

// IsSafeURL checks that a URL is safe to fetch (blocks private IPs, metadata endpoints).
func IsSafeURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	host := u.Hostname()
	if host == "" {
		return false
	}
	blocked := []string{"169.254.169.254", "metadata.google.internal", "100.100.100.200"}
	for _, b := range blocked {
		if host == b {
			return false
		}
	}
	ip := net.ParseIP(host)
	if ip != nil {
		return !isPrivateIP(ip)
	}
	addrs, err := net.LookupHost(host)
	if err != nil {
		return true
	}
	for _, addr := range addrs {
		if pip := net.ParseIP(addr); pip != nil && isPrivateIP(pip) {
			return false
		}
	}
	return true
}

func isPrivateIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}

// IsHTTPS returns true if the URL uses HTTPS.
func IsHTTPS(rawURL string) bool {
	return strings.HasPrefix(rawURL, "https://")
}
