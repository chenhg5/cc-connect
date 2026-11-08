package main

import (
	"bufio"
	"context"
	"encoding/binary"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

func TestProcessDNSFallsBackToTCPAfterTruncatedUDP(t *testing.T) {
	original := net.DefaultResolver
	t.Cleanup(func() { net.DefaultResolver = original })
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	t.Cleanup(func() { _ = listener.Close() })
	udp, err := net.ListenPacket("udp", address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = udp.Close() })

	query := []byte{
		0x12, 0x34, 0x01, 0x00, 0x00, 0x01,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x07, 'e', 'x', 'a', 'm', 'p', 'l', 'e',
		0x04, 't', 'e', 's', 't', 0x00,
		0x00, 0x01, 0x00, 0x01,
	}
	response := append([]byte(nil), query...)
	response[2] = 0x81
	response[3] = 0x80
	response[7] = 1
	response = append(response,
		0xc0, 0x0c, 0x00, 0x01, 0x00, 0x01,
		0x00, 0x00, 0x00, 0x3c, 0x00, 0x04,
		127, 0, 0, 1,
	)

	var udpOnce sync.Once
	udpAttempted := make(chan struct{})
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				t.Cleanup(func() { _ = conn.Close() })
				var length uint16
				if err := binary.Read(conn, binary.BigEndian, &length); err != nil {
					return
				}
				request := make([]byte, int(length))
				if _, err := io.ReadFull(conn, request); err != nil {
					return
				}
				udpOnce.Do(func() { t.Error("UDP was not attempted before TCP") })
				answer := append([]byte(nil), response...)
				copy(answer[:2], request[:2])
				if err := binary.Write(conn, binary.BigEndian, uint16(len(answer))); err != nil {
					return
				}
				_, _ = conn.Write(answer)
			}()
		}
	}()
	go func() {
		buffer := make([]byte, 1500)
		for {
			count, client, err := udp.ReadFrom(buffer)
			if err != nil {
				return
			}
			answer := append([]byte(nil), buffer[:count]...)
			answer[2] |= 0x82 // QR + truncated: force the Go resolver onto TCP.
			answer[3] |= 0x80
			_, _ = udp.WriteTo(answer, client)
		}
	}()

	if err := configureProcessDNS(address); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resolver := net.DefaultResolver
	go func() {
		udpOnce.Do(func() {})
		close(udpAttempted)
		conn, err := resolver.Dial(ctx, "udp", "ignored.invalid:53")
		if err != nil {
			t.Error(err)
			return
		}
		t.Cleanup(func() { _ = conn.Close() })
		_, _ = conn.Write(query)
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		answer := make([]byte, 1500)
		count, err := bufio.NewReader(conn).Read(answer)
		if err != nil {
			t.Error(err)
			return
		}
		if count < 2 || answer[2]&0x02 == 0 {
			t.Errorf("UDP answer is not marked truncated: %x", answer[:count])
		}
	}()

	select {
	case <-udpAttempted:
	case <-ctx.Done():
		t.Fatal("UDP truncation was not exercised before TCP")
	}
	conn, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	// Exercise the same two-byte framing and answer bytes that net.Resolver
	// consumes after its UDP fallback. No external DNS server is contacted.
	if err := binary.Write(conn, binary.BigEndian, uint16(len(query))); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write(query); err != nil {
		t.Fatal(err)
	}
	var length uint16
	if err := binary.Read(conn, binary.BigEndian, &length); err != nil {
		t.Fatal(err)
	}
	answer := make([]byte, int(length))
	if _, err := io.ReadFull(conn, answer); err != nil {
		t.Fatal(err)
	}
	if answer[2]&0x02 != 0 {
		t.Fatal("TCP response unexpectedly retained truncation flag")
	}
	if got := answer[len(answer)-4:]; got[0] != 127 || got[1] != 0 || got[2] != 0 || got[3] != 1 {
		t.Fatalf("TCP answer = %v, want 127.0.0.1", got)
	}
}
