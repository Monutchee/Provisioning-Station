// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

package tftp

import (
	"bytes"
	"context"
	"encoding/binary"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReadWithOptionNegotiation(t *testing.T) {
	root := t.TempDir()
	want := bytes.Repeat([]byte("station-data-"), 240)
	if err := os.WriteFile(filepath.Join(root, "Image"), want, 0o644); err != nil {
		t.Fatal(err)
	}
	config := DefaultConfig(root)
	config.ListenAddress = "127.0.0.1:0"
	config.Timeout = 100 * time.Millisecond
	server, err := Listen(config)
	if err != nil {
		t.Fatal(err)
	}
	context, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- server.Serve(context) }()

	client, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	request := rrqPacket("Image", [][2]string{{"blksize", "1024"}, {"timeout", "1"}, {"tsize", "0"}})
	if _, err := client.WriteToUDP(request, server.Addr().(*net.UDPAddr)); err != nil {
		t.Fatal(err)
	}

	buffer := make([]byte, 65535)
	client.SetReadDeadline(time.Now().Add(2 * time.Second))
	count, transferAddress, err := client.ReadFromUDP(buffer)
	if err != nil {
		t.Fatal(err)
	}
	if binary.BigEndian.Uint16(buffer[:2]) != opcodeOACK {
		t.Fatalf("first opcode = %d, want OACK; packet=%q", binary.BigEndian.Uint16(buffer[:2]), buffer[:count])
	}
	if !bytes.Contains(buffer[:count], []byte("tsize\x00")) {
		t.Fatalf("OACK lacks tsize: %q", buffer[:count])
	}
	if _, err := client.WriteToUDP(ackPacket(0), transferAddress); err != nil {
		t.Fatal(err)
	}

	var got []byte
	for {
		count, sender, err := client.ReadFromUDP(buffer)
		if err != nil {
			t.Fatal(err)
		}
		if !sender.IP.Equal(transferAddress.IP) || sender.Port != transferAddress.Port {
			continue
		}
		if count < 4 || binary.BigEndian.Uint16(buffer[:2]) != opcodeDATA {
			t.Fatalf("unexpected transfer packet: %v", buffer[:count])
		}
		block := binary.BigEndian.Uint16(buffer[2:4])
		got = append(got, buffer[4:count]...)
		if _, err := client.WriteToUDP(ackPacket(block), transferAddress); err != nil {
			t.Fatal(err)
		}
		if count-4 < 1024 {
			break
		}
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("downloaded %d bytes, want %d", len(got), len(want))
	}

	deadline := time.After(2 * time.Second)
	completed := false
	for !completed {
		select {
		case event := <-server.Events():
			if event.Filename == "Image" && event.Status == "completed" {
				completed = true
				if event.Bytes != int64(len(want)) {
					t.Fatalf("event bytes = %d, want %d", event.Bytes, len(want))
				}
			}
		case <-deadline:
			t.Fatal("timed out waiting for completed event")
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestTraversalRequestIsRejected(t *testing.T) {
	config := DefaultConfig(t.TempDir())
	config.ListenAddress = "127.0.0.1:0"
	server, err := Listen(config)
	if err != nil {
		t.Fatal(err)
	}
	context, cancel := context.WithCancel(context.Background())
	defer cancel()
	go server.Serve(context)

	client, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err := client.WriteToUDP(rrqPacket("../secret", nil), server.Addr().(*net.UDPAddr)); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 1024)
	client.SetReadDeadline(time.Now().Add(time.Second))
	count, _, err := client.ReadFromUDP(buffer)
	if err != nil {
		t.Fatal(err)
	}
	if count < 4 || binary.BigEndian.Uint16(buffer[:2]) != opcodeERROR {
		t.Fatalf("packet = %v, want ERROR", buffer[:count])
	}
}

func TestUnexpectedClientIPIsRejected(t *testing.T) {
	config := DefaultConfig(t.TempDir())
	config.ListenAddress = "127.0.0.1:0"
	config.AllowedClientIP = "192.0.2.20"
	server, err := Listen(config)
	if err != nil {
		t.Fatal(err)
	}
	context, cancel := context.WithCancel(context.Background())
	defer cancel()
	go server.Serve(context)

	client, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err := client.WriteToUDP(rrqPacket("Image", nil), server.Addr().(*net.UDPAddr)); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 1024)
	client.SetReadDeadline(time.Now().Add(time.Second))
	count, _, err := client.ReadFromUDP(buffer)
	if err != nil {
		t.Fatal(err)
	}
	if count < 4 || binary.BigEndian.Uint16(buffer[:2]) != opcodeERROR ||
		binary.BigEndian.Uint16(buffer[2:4]) != errorAccessViolation {
		t.Fatalf("packet = %v, want access violation", buffer[:count])
	}
}

func rrqPacket(filename string, options [][2]string) []byte {
	packet := []byte{0, opcodeRRQ}
	packet = append(packet, filename...)
	packet = append(packet, 0)
	packet = append(packet, "octet"...)
	packet = append(packet, 0)
	for _, option := range options {
		packet = append(packet, option[0]...)
		packet = append(packet, 0)
		packet = append(packet, option[1]...)
		packet = append(packet, 0)
	}
	return packet
}

func ackPacket(block uint16) []byte {
	packet := make([]byte, 4)
	binary.BigEndian.PutUint16(packet[:2], opcodeACK)
	binary.BigEndian.PutUint16(packet[2:], block)
	return packet
}
