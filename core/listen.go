package core

import (
	"net"
	"strconv"
	"strings"
)

const defaultLoopbackBind = "127.0.0.1"

func normalizeBind(bind string) string {
	bind = strings.TrimSpace(bind)
	if bind == "" {
		return defaultLoopbackBind
	}
	return bind
}

func listenAddress(bind string, port int) string {
	return net.JoinHostPort(normalizeBind(bind), strconv.Itoa(port))
}
