package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
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
	udp, err := net.ListenPacket("udp", address)
	if err != nil {
		_ = listener.Close()
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	t.Cleanup(func() {
		_ = listener.Close()
		_ = udp.Close()
		workers.Wait()
	})

	// Preserve the resolver's transaction ID and question, but omit its EDNS
	// additional record so the answer follows the question in a valid packet.
	question := []byte{
		0x07, 'e', 'x', 'a', 'm', 'p', 'l', 'e',
		0x04, 't', 'e', 's', 't', 0x00,
		0x00, 0x01, 0x00, 0x01,
	}
	response := func(query []byte, truncated bool) ([]byte, error) {
		end := 12 + len(question)
		if len(query) < end || binary.BigEndian.Uint16(query[4:6]) != 1 || !bytes.Equal(query[12:end], question) {
			return nil, fmt.Errorf("unexpected DNS query: %x", query)
		}
		answer := append([]byte(nil), query[:end]...)
		answer[2], answer[3] = 0x81, 0x80 // response, recursion desired/available
		clear(answer[6:12])
		if truncated {
			answer[2] |= 0x02
			return answer, nil
		}
		answer[7] = 1
		return append(answer,
			0xc0, 0x0c, 0x00, 0x01, 0x00, 0x01,
			0x00, 0x00, 0x00, 0x3c, 0x00, 0x04,
			127, 0, 0, 1,
		), nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	var udpReceived atomic.Bool
	results := make(chan error, 2)
	serve := func(fn func() error) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			results <- fn()
		}()
	}
	serve(func() error {
		buffer := make([]byte, 1500)
		count, client, err := udp.ReadFrom(buffer)
		if err != nil {
			return err
		}
		answer, err := response(buffer[:count], true)
		if err != nil {
			return err
		}
		udpReceived.Store(true)
		_, err = udp.WriteTo(answer, client)
		return err
	})
	serve(func() error {
		conn, err := listener.Accept()
		if err != nil {
			return err
		}
		defer func() { _ = conn.Close() }()
		if err := conn.SetDeadline(deadline); err != nil {
			return err
		}
		var length uint16
		if err := binary.Read(conn, binary.BigEndian, &length); err != nil {
			return err
		}
		query := make([]byte, int(length))
		if _, err := io.ReadFull(conn, query); err != nil {
			return err
		}
		if !udpReceived.Load() {
			return fmt.Errorf("TCP query arrived before the UDP query")
		}
		answer, err := response(query, false)
		if err != nil {
			return err
		}
		if err := binary.Write(conn, binary.BigEndian, uint16(len(answer))); err != nil {
			return err
		}
		_, err = conn.Write(answer)
		return err
	})

	if err := configureProcessDNS(address); err != nil {
		t.Fatal(err)
	}
	// ip4 requests one A record; the trailing dot avoids DNS search suffixes.
	// Both the UDP exchange and TCP retry must be performed by the resolver.
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip4", "example.test.")
	if err != nil {
		t.Fatalf("resolve through UDP-to-TCP fallback: %v", err)
	}
	if len(ips) != 1 || !ips[0].Equal(net.IPv4(127, 0, 0, 1)) {
		t.Fatalf("resolved IPs = %v, want 127.0.0.1", ips)
	}
	for range 2 {
		select {
		case err := <-results:
			if err != nil {
				t.Errorf("DNS fixture: %v", err)
			}
		case <-ctx.Done():
			t.Fatal("DNS fixture did not finish both transports")
		}
	}
}
