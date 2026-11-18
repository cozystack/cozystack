//go:build integration

package integration

import (
	"errors"
	"net"
	"testing"
)

func TestInterfaceIPv4(t *testing.T) {
	v4 := &net.IPNet{IP: net.ParseIP("192.0.2.10"), Mask: net.CIDRMask(24, 32)}
	v6 := &net.IPNet{IP: net.ParseIP("2001:db8::10"), Mask: net.CIDRMask(64, 128)}
	for _, tc := range []struct {
		name  string
		iface net.Interface
		addrs []net.Addr
		want  string
	}{
		{"macOS Ethernet", net.Interface{Name: "en0", Flags: net.FlagUp}, []net.Addr{v4}, "192.0.2.10"},
		{"Linux Ethernet", net.Interface{Name: "eth0", Flags: net.FlagUp}, []net.Addr{v4}, "192.0.2.10"},
		{"predictable Linux Ethernet", net.Interface{Name: "ens3", Flags: net.FlagUp}, []net.Addr{v4}, "192.0.2.10"},
		{"down", net.Interface{Name: "eth0"}, []net.Addr{v4}, ""},
		{"loopback", net.Interface{Name: "lo", Flags: net.FlagUp | net.FlagLoopback}, []net.Addr{v4}, ""},
		{"IPv6 only", net.Interface{Name: "eth0", Flags: net.FlagUp}, []net.Addr{v6}, ""},
		{"dual stack", net.Interface{Name: "eth0", Flags: net.FlagUp}, []net.Addr{v6, v4}, "192.0.2.10"},
		{"loopback address", net.Interface{Name: "eth0", Flags: net.FlagUp}, []net.Addr{&net.IPNet{IP: net.ParseIP("127.0.0.1"), Mask: net.CIDRMask(8, 32)}}, ""},
		{"link-local address", net.Interface{Name: "eth0", Flags: net.FlagUp}, []net.Addr{&net.IPNet{IP: net.ParseIP("169.254.1.10"), Mask: net.CIDRMask(16, 32)}}, ""},
		{"unspecified address", net.Interface{Name: "eth0", Flags: net.FlagUp}, []net.Addr{&net.IPNet{IP: net.ParseIP("0.0.0.0"), Mask: net.CIDRMask(0, 32)}}, ""},
		{"multicast address", net.Interface{Name: "eth0", Flags: net.FlagUp}, []net.Addr{&net.IPNet{IP: net.ParseIP("224.0.0.1"), Mask: net.CIDRMask(4, 32)}}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := interfaceIPv4(tc.iface, tc.addrs, nil); got != tc.want {
				t.Fatalf("IPv4 = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSelectAdvertiseIPv4(t *testing.T) {
	interfaces := []net.Interface{
		{Name: "lo", Flags: net.FlagUp | net.FlagLoopback},
		{Name: "unusable-first", Flags: net.FlagUp},
		{Name: "eth0", Flags: net.FlagUp},
		{Name: "en0", Flags: net.FlagUp},
	}
	addresses := func(iface net.Interface) ([]net.Addr, error) {
		ips := map[string][]string{
			"lo":             {"127.0.0.1"},
			"unusable-first": {"127.0.0.1", "169.254.1.10", "0.0.0.0", "224.0.0.1", "2001:db8::10"},
			"eth0":           {"192.0.2.10", "192.0.2.11"},
			"en0":            {"198.51.100.10"},
		}
		var addrs []net.Addr
		for _, ip := range ips[iface.Name] {
			addrs = append(addrs, &net.IPNet{IP: net.ParseIP(ip), Mask: net.CIDRMask(24, 32)})
		}
		return addrs, nil
	}
	for _, tc := range []struct {
		name       string
		interfaces []net.Interface
		requested  string
		want       string
	}{
		{"unusable first interface", interfaces, "", "192.0.2.10"},
		{"macOS interface only", interfaces[3:], "", "198.51.100.10"},
		{"explicit second address", interfaces, "192.0.2.11", "192.0.2.11"},
		{"explicit macOS address", interfaces, "198.51.100.10", "198.51.100.10"},
		{"unassigned address", interfaces, "203.0.113.10", ""},
		{"explicit loopback", interfaces, "127.0.0.1", ""},
		{"explicit link-local", interfaces, "169.254.1.10", ""},
		{"explicit IPv6", interfaces, "2001:db8::10", ""},
		{"invalid address", interfaces, "not-an-IP", ""},
		{"no usable IPv4", interfaces[:2], "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := selectAdvertiseIPv4(tc.interfaces, addresses, tc.requested)
			if got != tc.want || (err != nil) != (tc.want == "") {
				t.Fatalf("IPv4 = %q, error = %v, want %q", got, err, tc.want)
			}
		})
	}
	t.Run("address lookup error", func(t *testing.T) {
		lookupErr := errors.New("address lookup failed")
		_, err := selectAdvertiseIPv4(interfaces[2:], func(net.Interface) ([]net.Addr, error) {
			return nil, lookupErr
		}, "")
		if !errors.Is(err, lookupErr) {
			t.Fatalf("error = %v, want %v", err, lookupErr)
		}
	})
}
