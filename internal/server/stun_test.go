package server

import (
	"net"
	"testing"
	"time"

	"github.com/pion/stun/v3"
)

func TestSTUNReportsCallerAddress(t *testing.T) {
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	go ServeSTUN(conn)

	c, err := stun.Dial("udp", conn.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	done := make(chan error, 1)
	var got stun.XORMappedAddress
	err = c.Start(stun.MustBuild(stun.TransactionID, stun.BindingRequest), func(e stun.Event) {
		if e.Error == nil {
			e.Error = got.GetFrom(e.Message)
		}
		done <- e.Error
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil || !got.IP.IsLoopback() || got.Port == 0 {
			t.Fatalf("mapped address = %v, %v", got, err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no STUN answer")
	}
}
