package server

import (
	"net"

	"github.com/pion/stun/v3"
)

// ServeSTUN answers STUN binding requests, which is all a direct connection needs to
// learn its public address. It relays nothing, so it costs next to no traffic.
func ServeSTUN(conn net.PacketConn) error {
	buf := make([]byte, 1500)
	for {
		n, addr, err := conn.ReadFrom(buf)
		if err != nil {
			return err
		}
		req := &stun.Message{Raw: append([]byte(nil), buf[:n]...)}
		udp, ok := addr.(*net.UDPAddr)
		if !ok || req.Decode() != nil || req.Type != stun.BindingRequest {
			continue
		}
		res, err := stun.Build(stun.NewTransactionIDSetter(req.TransactionID), stun.BindingSuccess,
			&stun.XORMappedAddress{IP: udp.IP, Port: udp.Port}, stun.Fingerprint)
		if err == nil {
			_, _ = conn.WriteTo(res.Raw, addr)
		}
	}
}
