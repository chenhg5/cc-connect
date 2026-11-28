package main

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestProcessDNS(t *testing.T) {
	original := net.DefaultResolver
	t.Cleanup(func() { net.DefaultResolver = original })

	t.Run("empty preserves resolver", func(t *testing.T) {
		for _, value := range []string{"", "  \t"} {
			if err := configureProcessDNS(value); err != nil {
				t.Fatalf("configureProcessDNS(%q): %v", value, err)
			}
			if net.DefaultResolver != original {
				t.Fatalf("configureProcessDNS(%q) replaced the resolver", value)
			}
		}
	})

	t.Run("invalid settings preserve resolver", func(t *testing.T) {
		for _, value := range []string{
			"8.8.8.8",
			"8.8.8.8:abc",
			"8.8.8.8:0",
			"8.8.8.8:65536",
			"notanip:53",
			"2606:4700:4700::1111:53",
		} {
			if err := configureProcessDNS(value); err == nil {
				t.Errorf("configureProcessDNS(%q) succeeded, want error", value)
			}
			if net.DefaultResolver != original {
				t.Fatalf("configureProcessDNS(%q) replaced the resolver after an error", value)
			}
		}
	})

	t.Run("valid numeric addresses install resolver", func(t *testing.T) {
		for _, value := range []string{"1.1.1.1:53", "[2606:4700:4700::1111]:53"} {
			net.DefaultResolver = original
			if err := configureProcessDNS(value); err != nil {
				t.Fatalf("configureProcessDNS(%q): %v", value, err)
			}
			if net.DefaultResolver == original || net.DefaultResolver.Dial == nil || !net.DefaultResolver.PreferGo {
				t.Fatalf("configureProcessDNS(%q) did not install a Go resolver", value)
			}
		}
	})

	t.Run("resolver uses selected DNS server", func(t *testing.T) {
		server, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen for local DNS fixture: %v", err)
		}
		t.Cleanup(func() { _ = server.Close() })
		answers := make(chan string, 1)
		requestErrors := make(chan error, 1)
		go func() {
			buffer := make([]byte, 1500)
			for {
				count, client, err := server.ReadFrom(buffer)
				if err != nil {
					requestErrors <- err
					return
				}
				if count < 12 {
					continue
				}
				response := append([]byte(nil), buffer[:count]...)
				response[2] = buffer[2] | 0x80
				response[3] = buffer[3] | 0x80
				response[7] = 1
				if count >= 4 && buffer[count-4] == 0 && buffer[count-3] == 1 {
					response = append(response,
						0xc0, 0x0c, 0x00, 0x01, 0x00, 0x01,
						0x00, 0x00, 0x00, 0x3c, 0x00, 0x04,
						127, 0, 0, 1,
					)
				}
				if _, err := server.WriteTo(response, client); err != nil {
					requestErrors <- err
					return
				}
				select {
				case answers <- "response sent":
				default:
				}
			}
		}()

		if err := configureProcessDNS(server.LocalAddr().String()); err != nil {
			t.Fatalf("configure local resolver: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		conn, err := net.DefaultResolver.Dial(ctx, "udp", "ignored.invalid:53")
		if err != nil {
			t.Fatalf("dial through selected DNS server: %v", err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		if conn.RemoteAddr().String() != server.LocalAddr().String() {
			t.Fatalf("DNS connection target = %q, want %q", conn.RemoteAddr(), server.LocalAddr())
		}
		query := []byte{
			0x12, 0x34, 0x01, 0x00, 0x00, 0x01,
			0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
			0x07, 'e', 'x', 'a', 'm', 'p', 'l', 'e',
			0x04, 't', 'e', 's', 't', 0x00,
			0x00, 0x01, 0x00, 0x01,
		}
		if _, err := conn.Write(query); err != nil {
			t.Fatalf("send local DNS query: %v", err)
		}
		if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Fatalf("set local DNS read deadline: %v", err)
		}
		response := make([]byte, 1500)
		count, err := conn.Read(response)
		if err != nil {
			t.Fatalf("read local DNS response: %v", err)
		}
		response = response[:count]
		if len(response) < 16 || response[0] != 0x12 || response[1] != 0x34 || response[7] != 1 {
			t.Fatalf("local DNS response header = %x, want matching ID and one answer", response)
		}
		if got := response[len(response)-4:]; got[0] != 127 || got[1] != 0 || got[2] != 0 || got[3] != 1 {
			t.Fatalf("local DNS answer = %v, want 127.0.0.1", got)
		}
		select {
		case <-answers:
		case err := <-requestErrors:
			t.Fatalf("local DNS fixture: %v", err)
		case <-ctx.Done():
			t.Fatal("local DNS fixture did not answer")
		}
	})

	t.Run("dial observes cancellation", func(t *testing.T) {
		if err := configureProcessDNS("192.0.2.1:53"); err != nil {
			t.Fatalf("configure local resolver: %v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := net.DefaultResolver.Dial(ctx, "udp", "ignored.invalid:53"); err == nil {
			t.Fatal("Dial succeeded with a cancelled context")
		}
	})
}
