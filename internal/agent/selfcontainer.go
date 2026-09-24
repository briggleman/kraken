package agent

import (
	"os"
	"regexp"
	"sync"
)

// selfContainerID is the id of the container the Agent itself runs in, or ""
// when it runs on the host (the Windows service, a bare-metal Linux install)
// or the id cannot be read. The install guard uses it to leave the Agent's own
// container alone: under deploy/docker-compose.full.yml that container binds
// the whole data root, which is a parent of every server's data dir.
//
// Docker bind-mounts /etc/hostname, /etc/hosts and /etc/resolv.conf into every
// container from /var/lib/docker/containers/<id>/, and those mounts show up in
// /proc/self/mountinfo — including under network_mode: host, where the
// hostname is the host's and says nothing about the container.
var selfContainerID = sync.OnceValue(func() string {
	b, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return ""
	}
	return parseSelfContainerID(string(b))
})

var mountinfoContainerRE = regexp.MustCompile(`/containers/([0-9a-f]{64})/(?:hostname|hosts|resolv\.conf)`)

// parseSelfContainerID pulls the container id out of /proc/self/mountinfo
// text, or returns "".
func parseSelfContainerID(mountinfo string) string {
	if m := mountinfoContainerRE.FindStringSubmatch(mountinfo); m != nil {
		return m[1]
	}
	return ""
}
