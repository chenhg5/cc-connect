//go:build !linux

package containerhost

import "net"

func restrictPeer(listener net.Listener, _ int) net.Listener {
	return listener
}
