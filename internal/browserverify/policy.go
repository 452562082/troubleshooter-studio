package browserverify

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"

	"github.com/xiaolong/troubleshooter-studio/internal/bughub"
)

var (
	ErrBrowserDestinationBlocked     = errors.New("browser destination is blocked")
	ErrBrowserOriginNotAllowed       = errors.New("browser origin is not allowed")
	ErrBrowserProdInteractionBlocked = errors.New("browser interaction is blocked in production")
)

type IPResolver interface {
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}

var browserMetadataAddresses = map[netip.Addr]struct{}{
	netip.MustParseAddr("100.100.100.200"): {},
	netip.MustParseAddr("fd00:ec2::254"):   {},
}

var browserMetadataHosts = map[string]struct{}{
	"metadata":                     {},
	"metadata.google.internal":     {},
	"metadata.goog":                {},
	"instance-data":                {},
	"instance-data.ec2.internal":   {},
	"metadata.azure.internal":      {},
	"metadata.oraclecloud.com":     {},
	"metadata.packet.net":          {},
	"metadata.service.internal":    {},
	"metadata.tencentyun.com":      {},
	"metadata.tencentyun.internal": {},
}

func ValidatePlan(ctx context.Context, resolver IPResolver, policy bughub.BrowserSecurityPolicy, plan bughub.BrowserPlan) error {
	if err := requireConfiguredBrowserOrigin(plan.StartURL, policy.StartOrigins); err != nil {
		return err
	}
	if err := requireConfiguredBrowserOrigin(plan.StartURL, policy.ApplicationOrigins); err != nil {
		return err
	}
	if err := AllowedURL(ctx, resolver, policy, plan.StartURL); err != nil {
		return err
	}
	for _, action := range plan.Actions {
		if policy.IsProd && action.Action != "goto" && action.Action != "wait_for" && action.Action != "screenshot" {
			return fmt.Errorf("%w: action %s", ErrBrowserProdInteractionBlocked, action.ID)
		}
		if action.Action == "goto" {
			if err := AllowedURL(ctx, resolver, policy, action.URL); err != nil {
				return err
			}
		}
	}
	return nil
}

func requireConfiguredBrowserOrigin(rawURL string, configured []string) error {
	_, origin, _, err := parseBrowserURL(rawURL)
	if err != nil {
		return fmt.Errorf("%w: invalid URL", ErrBrowserDestinationBlocked)
	}
	if _, allowed := normalizedOriginSet(configured)[origin]; !allowed {
		return fmt.Errorf("%w: start or application origin", ErrBrowserOriginNotAllowed)
	}
	return nil
}

func AllowedURL(ctx context.Context, resolver IPResolver, policy bughub.BrowserSecurityPolicy, rawURL string) error {
	parsed, origin, host, err := parseBrowserURL(rawURL)
	if err != nil {
		return fmt.Errorf("%w: invalid URL", ErrBrowserDestinationBlocked)
	}
	_ = parsed

	if isBrowserMetadataHost(host) {
		return fmt.Errorf("%w: metadata host", ErrBrowserDestinationBlocked)
	}
	if _, allowed := normalizedOriginSet(appendOriginLists(policy.AllowedOrigins, policy.AuthOrigins))[origin]; !allowed {
		return fmt.Errorf("%w: destination origin", ErrBrowserOriginNotAllowed)
	}

	privateAllowed := false
	if _, ok := normalizedOriginSet(policy.PrivateOrigins)[origin]; ok {
		privateAllowed = true
	}
	if literal, err := netip.ParseAddr(host); err == nil {
		return validateBrowserAddress(literal, privateAllowed)
	}
	if resolver == nil {
		return fmt.Errorf("%w: DNS resolver unavailable", ErrBrowserDestinationBlocked)
	}
	addresses, err := resolver.LookupIPAddr(ctx, host)
	if err != nil || len(addresses) == 0 {
		return fmt.Errorf("%w: DNS resolution failed", ErrBrowserDestinationBlocked)
	}
	for _, candidate := range addresses {
		address, ok := netip.AddrFromSlice(candidate.IP)
		if !ok {
			return fmt.Errorf("%w: invalid DNS address", ErrBrowserDestinationBlocked)
		}
		if err := validateBrowserAddress(address, privateAllowed); err != nil {
			return err
		}
	}
	return nil
}

func appendOriginLists(first, second []string) []string {
	combined := make([]string, 0, len(first)+len(second))
	combined = append(combined, first...)
	combined = append(combined, second...)
	return combined
}

func normalizedOriginSet(origins []string) map[string]struct{} {
	result := make(map[string]struct{}, len(origins))
	for _, configured := range origins {
		_, origin, _, err := parseBrowserURL(strings.TrimSpace(configured))
		if err == nil {
			result[origin] = struct{}{}
		}
	}
	return result
}

func parseBrowserURL(raw string) (*url.URL, string, string, error) {
	if raw == "" || strings.TrimSpace(raw) != raw {
		return nil, "", "", errors.New("URL is empty or padded")
	}
	parsed, err := url.Parse(raw)
	if err != nil || !parsed.IsAbs() || parsed.Opaque != "" {
		return nil, "", "", errors.New("URL is not absolute")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return nil, "", "", errors.New("URL scheme is forbidden")
	}
	if parsed.User != nil {
		return nil, "", "", errors.New("URL userinfo is forbidden")
	}
	host := strings.ToLower(strings.TrimRight(parsed.Hostname(), "."))
	if host == "" || strings.Contains(host, "%") {
		return nil, "", "", errors.New("URL host is invalid")
	}
	if strings.HasSuffix(parsed.Host, ":") {
		return nil, "", "", errors.New("URL port is empty")
	}
	port := parsed.Port()
	if port == "" {
		if scheme == "http" {
			port = "80"
		} else {
			port = "443"
		}
	} else {
		numericPort, err := strconv.ParseUint(port, 10, 16)
		if err != nil || numericPort == 0 {
			return nil, "", "", errors.New("URL port is invalid")
		}
		port = strconv.FormatUint(numericPort, 10)
	}
	return parsed, scheme + "://" + net.JoinHostPort(host, port), host, nil
}

func isBrowserMetadataHost(host string) bool {
	_, blocked := browserMetadataHosts[strings.TrimRight(strings.ToLower(host), ".")]
	return blocked
}

func validateBrowserAddress(address netip.Addr, privateAllowed bool) error {
	if !address.IsValid() {
		return fmt.Errorf("%w: invalid IP address", ErrBrowserDestinationBlocked)
	}
	address = address.Unmap()
	if _, metadata := browserMetadataAddresses[address]; metadata {
		return fmt.Errorf("%w: metadata IP address", ErrBrowserDestinationBlocked)
	}
	if address.IsUnspecified() || address.IsMulticast() || address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() {
		return fmt.Errorf("%w: non-routable IP address", ErrBrowserDestinationBlocked)
	}
	if (address.IsPrivate() || address.IsLoopback()) && !privateAllowed {
		return fmt.Errorf("%w: private IP address requires exact origin opt-in", ErrBrowserDestinationBlocked)
	}
	return nil
}
