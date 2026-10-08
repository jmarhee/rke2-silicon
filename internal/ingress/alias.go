package ingress

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
)

// EnsureAlias adds ip as a /32 alias on the LAN interface whose subnet
// contains it. A host route makes the Mac deliver the address locally and
// still answer ARP for it. Ports are not opened here.
func EnsureAlias(ip string) error {
	parsed := net.ParseIP(ip).To4()
	if parsed == nil {
		return fmt.Errorf("ingress-address %q is not an IPv4 address", ip)
	}
	iface, err := lanInterface(parsed)
	if err != nil {
		return err
	}
	bits, on, err := assignedPrefix(parsed)
	if err != nil {
		return err
	}
	if on && bits == 32 {
		return nil
	}
	if on {
		if err := runIfconfig([]string{iface, "-alias", parsed.String()}); err != nil {
			return err
		}
	}
	clearARP(parsed.String())
	cidr := parsed.String() + "/32"
	if err := runIfconfig([]string{iface, "alias", cidr}); err != nil {
		return fmt.Errorf("add %s on %s: %w; run: sudo ifconfig %s alias %s", ip, iface, err, iface, cidr)
	}
	clearARP(parsed.String())
	return nil
}

// RemoveAlias deletes ip when it is assigned on an interface.
func RemoveAlias(ip string) error {
	if ip == "" {
		return nil
	}
	parsed := net.ParseIP(ip).To4()
	if parsed == nil {
		return fmt.Errorf("ingress-address %q is not an IPv4 address", ip)
	}
	_, on, err := assignedPrefix(parsed)
	if err != nil {
		return err
	}
	if !on {
		return nil
	}
	iface, err := interfaceWithAddress(parsed)
	if err != nil {
		return err
	}
	if err := runIfconfig([]string{iface, "-alias", parsed.String()}); err != nil {
		return fmt.Errorf("remove %s from %s: %w", ip, iface, err)
	}
	return nil
}

func assignedPrefix(ip net.IP) (int, bool, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return 0, false, err
	}
	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			return 0, false, err
		}
		for _, addr := range addrs {
			ipnet, ok := addr.(*net.IPNet)
			if !ok || ipnet.IP.To4() == nil || !ipnet.IP.To4().Equal(ip) {
				continue
			}
			ones, _ := ipnet.Mask.Size()
			return ones, true, nil
		}
	}
	return 0, false, nil
}

func interfaceWithAddress(ip net.IP) (string, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "", err
	}
	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			return "", err
		}
		for _, addr := range addrs {
			ipnet, ok := addr.(*net.IPNet)
			if !ok || ipnet.IP.To4() == nil {
				continue
			}
			if ipnet.IP.To4().Equal(ip) {
				return iface.Name, nil
			}
		}
	}
	return "", fmt.Errorf("no interface has address %s", ip)
}

func lanInterface(ip net.IP) (string, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "", err
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			return "", err
		}
		for _, addr := range addrs {
			ipnet, ok := addr.(*net.IPNet)
			if !ok || ipnet.IP.To4() == nil {
				continue
			}
			if ipnet.Contains(ip) && !ipnet.IP.To4().Equal(ip) {
				return iface.Name, nil
			}
		}
	}
	return "", fmt.Errorf("no interface has a subnet containing %s", ip)
}

func clearARP(ip string) {
	_ = commandError(exec.Command("/usr/sbin/arp", "-d", ip))
	_ = commandError(exec.Command("sudo", append([]string{"-n", "/usr/sbin/arp", "-d", ip})...))
}

func runIfconfig(args []string) error {
	if os.Geteuid() == 0 {
		return commandError(exec.Command("/sbin/ifconfig", args...))
	}
	if err := commandError(exec.Command("/sbin/ifconfig", args...)); err == nil {
		return nil
	}
	return commandError(exec.Command("sudo", append([]string{"-n", "/sbin/ifconfig"}, args...)...))
}

func commandError(cmd *exec.Cmd) error {
	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	msg := strings.TrimSpace(string(out))
	if msg == "" {
		return err
	}
	return fmt.Errorf("%s", msg)
}
