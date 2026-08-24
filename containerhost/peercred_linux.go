//go:build linux

package containerhost

import (
	"net"
	"syscall"

	"golang.org/x/sys/unix"
)

type peerListener struct {
	net.Listener
	uid int
}

func restrictPeer(listener net.Listener, uid int) net.Listener {
	return &peerListener{Listener: listener, uid: uid}
}

func (l *peerListener) Accept() (net.Conn, error) {
	for {
		connection, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		unixConnection, ok := connection.(*net.UnixConn)
		if ok && allowedPeer(unixConnection, l.uid) {
			return connection, nil
		}
		_ = connection.Close()
	}
}

func allowedPeer(connection *net.UnixConn, uid int) bool {
	raw, err := connection.SyscallConn()
	if err != nil {
		return false
	}
	allowed := false
	controlErr := raw.Control(func(fd uintptr) {
		credentials, credentialErr := unix.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
		allowed = credentialErr == nil && int(credentials.Uid) == uid
	})
	return controlErr == nil && allowed
}
