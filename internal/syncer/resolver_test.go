package syncer

import (
	"context"
	"fmt"
	"net"
	"reflect"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func TestResolveHostnames_AcceptsLiteralIPv4AndCIDR(t *testing.T) {
	hostIPs, err := ResolveHostnames("\n# comment\n203.0.113.10\n198.51.100.0/24\n203.0.113.10 # duplicate\n", nil)
	if err != nil {
		t.Fatalf("ResolveHostnames returned error: %v", err)
	}

	if got := hostIPs["203.0.113.10"]; got != "203.0.113.10" {
		t.Fatalf("literal IPv4 source = %q, want %q", got, "203.0.113.10")
	}
	if got := hostIPs["198.51.100.0/24"]; got != "198.51.100.0/24" {
		t.Fatalf("CIDR source = %q, want %q", got, "198.51.100.0/24")
	}

	gotIPs := SortedIPs(hostIPs)
	wantIPs := []string{"198.51.100.0/24", "203.0.113.10"}
	if !reflect.DeepEqual(gotIPs, wantIPs) {
		t.Fatalf("SortedIPs = %v, want %v", gotIPs, wantIPs)
	}
}

func TestMultiResolverResolveIPv4_UsesAuthoritativeAndPublicResolvers(t *testing.T) {
	const (
		public1 = "1.1.1.1:53"
		public2 = "8.8.8.8:53"
		auth1   = "203.0.113.53:53"
	)

	resolver := &multiResolver{
		publicServers: []string{public1, public2},
		queryTimeout:  time.Second,
		exchange: fakeDNSExchange(map[fakeDNSKey]*dns.Msg{
			{server: public1, qname: "app.example.com.", qtype: dns.TypeNS}: dnsSuccess("app.example.com."),
			{server: public2, qname: "app.example.com.", qtype: dns.TypeNS}: dnsSuccess("app.example.com."),
			{server: public1, qname: "example.com.", qtype: dns.TypeNS}:     dnsSuccess("example.com.", &dns.NS{Hdr: dns.RR_Header{Name: "example.com.", Rrtype: dns.TypeNS, Class: dns.ClassINET, Ttl: 60}, Ns: "ns1.example.net."}),
			{server: public2, qname: "example.com.", qtype: dns.TypeNS}:     dnsSuccess("example.com."),
			{server: public1, qname: "ns1.example.net.", qtype: dns.TypeA}:  dnsSuccess("ns1.example.net.", &dns.A{Hdr: dns.RR_Header{Name: "ns1.example.net.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60}, A: mustIPv4(t, "203.0.113.53")}),
			{server: public2, qname: "ns1.example.net.", qtype: dns.TypeA}:  dnsSuccess("ns1.example.net."),
			{server: auth1, qname: "app.example.com.", qtype: dns.TypeA}:    dnsSuccess("app.example.com.", &dns.A{Hdr: dns.RR_Header{Name: "app.example.com.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60}, A: mustIPv4(t, "198.51.100.10")}),
			{server: public1, qname: "app.example.com.", qtype: dns.TypeA}:  dnsSuccess("app.example.com.", &dns.A{Hdr: dns.RR_Header{Name: "app.example.com.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60}, A: mustIPv4(t, "198.51.100.11")}),
			{server: public2, qname: "app.example.com.", qtype: dns.TypeA}:  dnsSuccess("app.example.com."),
		}),
	}

	got, err := resolver.ResolveIPv4("app.example.com")
	if err != nil {
		t.Fatalf("ResolveIPv4 returned error: %v", err)
	}

	want := []string{"198.51.100.10", "198.51.100.11"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ResolveIPv4 = %v, want %v", got, want)
	}
}

type fakeDNSKey struct {
	server string
	qname  string
	qtype  uint16
}

func fakeDNSExchange(responses map[fakeDNSKey]*dns.Msg) dnsExchangeFunc {
	return func(ctx context.Context, server, qname string, qtype uint16) (*dns.Msg, error) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		key := fakeDNSKey{server: server, qname: dns.Fqdn(qname), qtype: qtype}
		msg, ok := responses[key]
		if !ok {
			return nil, fmt.Errorf("unexpected query server=%s name=%s type=%d", server, qname, qtype)
		}
		return msg.Copy(), nil
	}
}

func dnsSuccess(qname string, answers ...dns.RR) *dns.Msg {
	msg := new(dns.Msg)
	msg.SetQuestion(dns.Fqdn(qname), dns.TypeA)
	msg.Rcode = dns.RcodeSuccess
	msg.Answer = append(msg.Answer, answers...)
	return msg
}

func mustIPv4(t *testing.T, value string) net.IP {
	t.Helper()
	ip := net.ParseIP(value)
	if ip == nil {
		t.Fatalf("failed to parse IPv4 %q", value)
	}
	return ip.To4()
}
