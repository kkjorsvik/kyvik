package netproxy

import (
	"fmt"
	"net"
	"slices"
)

// privateRanges are RFC 1918, loopback, link-local, and other non-routable ranges.
var privateRanges []*net.IPNet

func init() {
	for _, cidr := range []string{
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"127.0.0.0/8",
		"169.254.0.0/16",
		"::1/128",
		"fc00::/7",
		"fe80::/10",
		"0.0.0.0/8",
	} {
		_, network, _ := net.ParseCIDR(cidr)
		privateRanges = append(privateRanges, network)
	}
}

// isPrivateIP returns true if the IP is in a private/non-routable range.
func isPrivateIP(ip net.IP) bool {
	for _, r := range privateRanges {
		if r.Contains(ip) {
			return true
		}
	}
	return false
}

// checkSSRF resolves the hostname and validates none of the IPs are private.
// Returns nil if the host is safe, or an error describing the SSRF violation.
func checkSSRF(host string) error {
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("DNS resolution failed for %s: %w", host, err)
	}
	for _, ip := range ips {
		if isPrivateIP(ip) {
			return fmt.Errorf("blocked: %s resolves to private IP %s", host, ip)
		}
	}
	return nil
}

// isHostAllowed checks if a hostname is in the allowlist.
// Empty allowlist means all hosts are allowed.
func isHostAllowed(host string, allowedHosts []string) bool {
	if len(allowedHosts) == 0 {
		return true
	}
	return slices.Contains(allowedHosts, host)
}
