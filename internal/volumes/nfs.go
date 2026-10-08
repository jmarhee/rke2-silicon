package volumes

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// HostMounter mounts an NFS export with mount_nfs.
type HostMounter struct{}

// EnsureNFS mounts server:export on dest. A directory that is already an NFS
// mount is left in place.
func (HostMounter) EnsureNFS(server, export, dest string) error {
	mounted, err := nfsMounted(dest)
	if err != nil {
		return err
	}
	if mounted {
		return nil
	}
	if err := os.MkdirAll(dest, 0o777); err != nil {
		return err
	}
	spec := server + ":" + export
	cmd := exec.Command("/sbin/mount_nfs", "-o", "nolocks,resvport,nfsv3", spec, dest)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("mount_nfs %s: %s; the LaunchAgent user must be permitted to mount NFS", spec, msg)
	}
	return nil
}

func nfsMounted(dest string) (bool, error) {
	out, err := exec.Command("/sbin/mount").Output()
	if err != nil {
		return false, err
	}
	needle := " on " + dest + " ("
	return strings.Contains(string(out), needle), nil
}
