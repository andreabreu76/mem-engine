package main

import (
	"errors"
	"net"
	"testing"
)

func TestIsAddrInUse(t *testing.T) {
	held, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	ln, err := net.Listen("tcp4", held.Addr().String())
	if err == nil {
		ln.Close()
		t.Fatal("second listen on a held port succeeded")
	}
	if !isAddrInUse(err) {
		t.Fatalf("isAddrInUse(%v) = false, want true", err)
	}
	if isAddrInUse(errors.New("other")) || isAddrInUse(nil) {
		t.Fatal("isAddrInUse matches unrelated errors")
	}
}
