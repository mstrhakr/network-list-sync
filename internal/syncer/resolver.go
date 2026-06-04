package syncer

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"

	"github.com/miekg/dns"
)

const (
	defaultDNSQueryTimeout = 5 * time.Second
	maxCNAMEFollowDepth    = 8
)

var defaultPublicDNSServers = []string{
	"1.1.1.1:53",
	"1.0.0.1:53",
	"8.8.8.8:53",
	"8.8.4.4:53",
	"9.9.9.9:53",
	"149.112.112.112:53",
	"208.67.222.222:53",
	"208.67.220.220:53",
}

type dnsExchangeFunc func(ctx context.Context, server, qname string, qtype uint16) (*dns.Msg, error)

type multiResolver struct {
	publicServers []string
	queryTimeout  time.Duration
	exchange      dnsExchangeFunc
}

// newMultiResolverWithServers builds a resolver that queries exactly the provided servers.
// Returns an error if the list is empty — callers must ensure at least one server is configured.
func newMultiResolverWithServers(servers []string) (*multiResolver, error) {
	servers = uniqueStrings(servers)
	if len(servers) == 0 {
		return nil, fmt.Errorf("no DNS servers configured: add at least one enabled DNS server")
	}
	return &multiResolver{
		publicServers: servers,
		queryTimeout:  defaultDNSQueryTimeout,
		exchange:      exchangeDNS,
	}, nil
}

// ResolveHostnames resolves a newline-separated list of hostnames, IPv4 addresses,
// or IPv4 CIDR ranges into a de-duplicated set of IPv4 entries.
// servers is the complete list of DNS server addresses to query; an empty list is an error.
func ResolveHostnames(hostnamesText string, servers []string) (map[string]string, error) {
	result := make(map[string]string)
	var resolver *multiResolver
	lines := strings.Split(hostnamesText, "\n")
	var errors []string

	for _, rawLine := range lines {
		line := normalizeHostnameLine(rawLine)
		if line == "" {
			continue
		}

		if ip, ok := normalizeIPv4Literal(line); ok {
			addResolvedSource(result, ip, line)
			continue
		}

		if cidr, ok := normalizeIPv4CIDR(line); ok {
			addResolvedSource(result, cidr, line)
			continue
		}

		if resolver == nil {
			var err error
			resolver, err = newMultiResolverWithServers(servers)
			if err != nil {
				return nil, err
			}
		}

		ips, err := resolver.ResolveIPv4(line)
		if err != nil {
			errors = append(errors, err.Error())
			continue
		}

		for _, ip := range ips {
			addResolvedSource(result, ip, line)
		}
	}

	if len(result) == 0 {
		errMsg := "no IPv4 addresses resolved from hostname list"
		if len(errors) > 0 {
			errMsg += ": " + strings.Join(errors, "; ")
		}
		return nil, fmt.Errorf("%s", errMsg)
	}

	return result, nil
}

func (r *multiResolver) ResolveIPv4(hostname string) ([]string, error) {
	hostname = normalizeDNSName(hostname)
	ctx, cancel := context.WithTimeout(context.Background(), r.queryTimeout)
	defer cancel()

	publicServers := uniqueStrings(r.publicServers)
	authoritativeServers, authErr := lookupAuthoritativeServers(ctx, hostname, publicServers, r.exchange)
	servers := uniqueStrings(append(authoritativeServers, publicServers...))

	ips, err := lookupAWithServers(ctx, hostname, servers, r.exchange, 0)
	if err == nil {
		return ips, nil
	}

	if authErr != nil {
		return nil, fmt.Errorf("%s: %v (authoritative discovery: %v)", hostname, err, authErr)
	}

	return nil, fmt.Errorf("%s: %w", hostname, err)
}

func lookupAuthoritativeServers(ctx context.Context, hostname string, bootstrapServers []string, exchange dnsExchangeFunc) ([]string, error) {
	if len(bootstrapServers) == 0 {
		return nil, fmt.Errorf("no bootstrap DNS servers configured")
	}

	zones := zoneCandidates(hostname)
	var errors []string

	for _, zone := range zones {
		nsHosts, err := lookupNSHosts(ctx, zone, bootstrapServers, exchange)
		if err != nil {
			errors = append(errors, fmt.Sprintf("%s: %v", strings.TrimSuffix(zone, "."), err))
			continue
		}

		var authoritativeIPs []string
		for _, nsHost := range nsHosts {
			ips, err := lookupAWithServers(ctx, nsHost, bootstrapServers, exchange, 0)
			if err != nil {
				errors = append(errors, fmt.Sprintf("%s: %v", nsHost, err))
				continue
			}
			for _, ip := range ips {
				authoritativeIPs = append(authoritativeIPs, net.JoinHostPort(ip, "53"))
			}
		}

		authoritativeIPs = uniqueStrings(authoritativeIPs)
		if len(authoritativeIPs) > 0 {
			return authoritativeIPs, nil
		}
	}

	if len(errors) == 0 {
		return nil, fmt.Errorf("no authoritative name servers discovered")
	}

	return nil, fmt.Errorf("%s", strings.Join(errors, "; "))
}

func lookupNSHosts(ctx context.Context, zone string, servers []string, exchange dnsExchangeFunc) ([]string, error) {
	zone = dns.Fqdn(zone)
	var nsHosts []string
	var errors []string

	for _, server := range uniqueStrings(servers) {
		msg, err := exchange(ctx, server, zone, dns.TypeNS)
		if err != nil {
			errors = append(errors, fmt.Sprintf("%s: %v", server, err))
			continue
		}
		if msg == nil {
			errors = append(errors, fmt.Sprintf("%s: empty response", server))
			continue
		}
		if msg.Rcode != dns.RcodeSuccess {
			errors = append(errors, fmt.Sprintf("%s: rcode=%s", server, dns.RcodeToString[msg.Rcode]))
			continue
		}

		for _, answer := range msg.Answer {
			ns, ok := answer.(*dns.NS)
			if !ok {
				continue
			}
			nsHosts = append(nsHosts, normalizeDNSName(ns.Ns))
		}
	}

	nsHosts = uniqueStrings(nsHosts)
	if len(nsHosts) == 0 {
		if len(errors) == 0 {
			return nil, fmt.Errorf("no NS records returned")
		}
		return nil, fmt.Errorf("%s", strings.Join(errors, "; "))
	}

	return nsHosts, nil
}

func lookupAWithServers(ctx context.Context, hostname string, servers []string, exchange dnsExchangeFunc, depth int) ([]string, error) {
	if depth > maxCNAMEFollowDepth {
		return nil, fmt.Errorf("CNAME chain exceeded %d hops", maxCNAMEFollowDepth)
	}

	servers = uniqueStrings(servers)
	if len(servers) == 0 {
		return nil, fmt.Errorf("no DNS servers configured")
	}

	qname := dns.Fqdn(hostname)
	addresses := make(map[string]struct{})
	aliases := make(map[string]struct{})
	var errors []string

	for _, server := range servers {
		msg, err := exchange(ctx, server, qname, dns.TypeA)
		if err != nil {
			errors = append(errors, fmt.Sprintf("%s: %v", server, err))
			continue
		}
		if msg == nil {
			errors = append(errors, fmt.Sprintf("%s: empty response", server))
			continue
		}
		if msg.Rcode != dns.RcodeSuccess {
			errors = append(errors, fmt.Sprintf("%s: rcode=%s", server, dns.RcodeToString[msg.Rcode]))
			continue
		}

		for _, answer := range msg.Answer {
			switch rr := answer.(type) {
			case *dns.A:
				if rr.A != nil && rr.A.To4() != nil {
					addresses[rr.A.String()] = struct{}{}
				}
			case *dns.CNAME:
				aliases[normalizeDNSName(rr.Target)] = struct{}{}
			}
		}
	}

	if len(addresses) == 0 && len(aliases) > 0 {
		for alias := range aliases {
			aliasIPs, err := lookupAWithServers(ctx, alias, servers, exchange, depth+1)
			if err != nil {
				errors = append(errors, fmt.Sprintf("%s: %v", alias, err))
				continue
			}
			for _, ip := range aliasIPs {
				addresses[ip] = struct{}{}
			}
		}
	}

	if len(addresses) == 0 {
		if len(errors) == 0 {
			return nil, fmt.Errorf("no IPv4 A records returned")
		}
		return nil, fmt.Errorf("%s", strings.Join(errors, "; "))
	}

	ips := make([]string, 0, len(addresses))
	for ip := range addresses {
		ips = append(ips, ip)
	}
	sort.Slice(ips, func(i, j int) bool {
		return bytesLessIP(ips[i], ips[j])
	})
	return ips, nil
}

func exchangeDNS(ctx context.Context, server, qname string, qtype uint16) (*dns.Msg, error) {
	server = ensureDNSServerPort(server)
	msg := new(dns.Msg)
	msg.SetQuestion(dns.Fqdn(qname), qtype)
	msg.RecursionDesired = true

	udpClient := &dns.Client{Net: "udp", Timeout: 2 * time.Second}
	response, _, err := udpClient.ExchangeContext(ctx, msg, server)
	if err != nil {
		return nil, err
	}
	if response != nil && response.Truncated {
		tcpClient := &dns.Client{Net: "tcp", Timeout: 2 * time.Second}
		tcpResponse, _, err := tcpClient.ExchangeContext(ctx, msg, server)
		return tcpResponse, err
	}
	return response, nil
}

func zoneCandidates(hostname string) []string {
	labels := dns.SplitDomainName(dns.Fqdn(hostname))
	candidates := make([]string, 0, len(labels))
	for i := 0; i < len(labels); i++ {
		candidates = append(candidates, strings.Join(labels[i:], ".")+".")
	}
	return candidates
}

func normalizeHostnameLine(line string) string {
	line = strings.TrimSpace(line)
	if line == "" {
		return ""
	}
	if idx := strings.Index(line, "#"); idx >= 0 {
		line = strings.TrimSpace(line[:idx])
	}
	return line
}

func normalizeIPv4Literal(value string) (string, bool) {
	ip := net.ParseIP(strings.TrimSpace(value))
	if ip == nil || ip.To4() == nil {
		return "", false
	}
	return ip.To4().String(), true
}

func normalizeIPv4CIDR(value string) (string, bool) {
	_, network, err := net.ParseCIDR(strings.TrimSpace(value))
	if err != nil || network == nil || network.IP.To4() == nil {
		return "", false
	}
	return network.String(), true
}

func normalizeDNSName(name string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(name)), ".")
}

func ensureDNSServerPort(server string) string {
	if _, _, err := net.SplitHostPort(server); err == nil {
		return server
	}
	return net.JoinHostPort(server, "53")
}

func addResolvedSource(result map[string]string, entry, source string) {
	existing := result[entry]
	if existing == "" {
		result[entry] = source
		return
	}
	for _, part := range strings.Split(existing, ", ") {
		if part == source {
			return
		}
	}
	result[entry] = existing + ", " + source
}

func uniqueStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	unique := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		unique = append(unique, value)
	}
	return unique
}

func bytesLessIP(left, right string) bool {
	leftKey, leftPrefix, leftOK := ipSortKey(left)
	rightKey, rightPrefix, rightOK := ipSortKey(right)
	switch {
	case leftOK && rightOK:
		if compare := bytesCompare(leftKey, rightKey); compare != 0 {
			return compare < 0
		}
		return leftPrefix < rightPrefix
	case leftOK:
		return true
	case rightOK:
		return false
	default:
		return left < right
	}
}

func ipSortKey(value string) ([]byte, int, bool) {
	if ip := net.ParseIP(value); ip != nil && ip.To4() != nil {
		return ip.To4(), 32, true
	}
	_, network, err := net.ParseCIDR(value)
	if err != nil || network == nil || network.IP.To4() == nil {
		return nil, 0, false
	}
	ones, _ := network.Mask.Size()
	return network.IP.To4(), ones, true
}

func bytesCompare(left, right []byte) int {
	for i := 0; i < len(left) && i < len(right); i++ {
		if left[i] < right[i] {
			return -1
		}
		if left[i] > right[i] {
			return 1
		}
	}
	switch {
	case len(left) < len(right):
		return -1
	case len(left) > len(right):
		return 1
	default:
		return 0
	}
}
