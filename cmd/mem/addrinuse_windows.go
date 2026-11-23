package main

import (
	"errors"
	"syscall"
)

const wsaeaddrinuse = syscall.Errno(10048)

func isAddrInUse(err error) bool {
	return errors.Is(err, wsaeaddrinuse)
}
