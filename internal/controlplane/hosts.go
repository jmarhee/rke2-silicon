package controlplane

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

// RenderHosts builds an /etc/hosts file. Names that share an address are
// grouped, and both names and addresses are sorted so tests are stable.
func RenderHosts(nameToIP map[string]string) string {
	ipNames := map[string][]string{}
	var ips []string
	for name, ip := range nameToIP {
		if _, ok := ipNames[ip]; !ok {
			ips = append(ips, ip)
		}
		ipNames[ip] = append(ipNames[ip], name)
	}
	sort.Strings(ips)
	var b strings.Builder
	b.WriteString("127.0.0.1 localhost\n")
	for _, ip := range ips {
		names := ipNames[ip]
		sort.Strings(names)
		fmt.Fprintf(&b, "%s %s\n", ip, strings.Join(names, " "))
	}
	return b.String()
}

func writeFileSync(path, data string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
