// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

package serialconsole

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

type staticDiscoverer struct{ result Discovery }

func (discoverer staticDiscoverer) Discover(context.Context) (Discovery, error) {
	return discoverer.result, nil
}

type fakePort struct {
	reads  chan []byte
	closed chan struct{}
	once   sync.Once
	mutex  sync.Mutex
	writes []byte
}

func newFakePort() *fakePort {
	return &fakePort{reads: make(chan []byte, 8), closed: make(chan struct{})}
}

func (port *fakePort) Read(destination []byte) (int, error) {
	select {
	case data := <-port.reads:
		return copy(destination, data), nil
	case <-port.closed:
		return 0, io.EOF
	}
}

func (port *fakePort) Write(data []byte) (int, error) {
	port.mutex.Lock()
	defer port.mutex.Unlock()
	port.writes = append(port.writes, data...)
	return len(data), nil
}

func (port *fakePort) Close() error {
	port.once.Do(func() { close(port.closed) })
	return nil
}

func testManager(t *testing.T) (*Manager, Port, *fakePort) {
	t.Helper()
	port := newPort(candidate{
		Name: "/dev/ttyUSB1", VendorID: FTDIVendorID, ProductID: FT2232HProductID,
		USBSerial: "BOARD-A", Channel: FT2232HUARTChannel,
	})
	handle := newFakePort()
	manager, err := New(Config{
		Discoverer:  staticDiscoverer{Discovery{Ports: []Port{port}}},
		Open:        func(string, int) (PortHandle, error) { return handle, nil },
		ReplayBytes: 64,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	return manager, port, handle
}

func TestManagerSharesPortWithObserversAndOneController(t *testing.T) {
	manager, port, handle := testManager(t)
	controller, err := manager.Attach(context.Background(), AttachRequest{
		PortID: port.ID, Access: AccessController,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	observer, err := manager.Attach(context.Background(), AttachRequest{
		PortID: port.ID, Access: AccessObserver,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Close()
	discovery, err := manager.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(discovery.Ports) != 1 || !discovery.Ports[0].Busy || !discovery.Ports[0].HasController ||
		discovery.Ports[0].ActiveBaudRate != 115200 {
		t.Fatalf("active port = %+v", discovery.Ports)
	}
	if _, err := manager.Attach(context.Background(), AttachRequest{
		PortID: port.ID, Access: AccessController,
	}); !errors.Is(err, ErrControllerBusy) {
		t.Fatalf("second controller error = %v", err)
	}
	if err := controller.Write([]byte("help\r")); err != nil {
		t.Fatal(err)
	}
	if err := observer.Write([]byte("bad")); err == nil {
		t.Fatal("observer write succeeded")
	}
	handle.reads <- []byte("booting\r\n")
	for name, data := range map[string]<-chan []byte{"controller": controller.Data(), "observer": observer.Data()} {
		select {
		case got := <-data:
			if string(got) != "booting\r\n" {
				t.Fatalf("%s got %q", name, got)
			}
		case <-time.After(time.Second):
			t.Fatalf("%s received no data", name)
		}
	}
	handle.mutex.Lock()
	written := string(handle.writes)
	handle.mutex.Unlock()
	if written != "help\r" {
		t.Fatalf("writes = %q", written)
	}
}

func TestManagerMatchesOnlyUniqueFTDIChannelBPorts(t *testing.T) {
	discovery := normalizeCandidates([]candidate{
		{Name: "COM3", VendorID: FTDIVendorID, ProductID: FT2232HProductID, USBSerial: "BOARD-A", Channel: "B"},
		{Name: "COM4", VendorID: FTDIVendorID, ProductID: FT2232HProductID, USBSerial: "BOARD-B", Channel: "B"},
		{Name: "COM5", VendorID: FTDIVendorID, ProductID: FT2232HProductID, USBSerial: "BOARD-B", Channel: "B"},
		{Name: "COM6", USBSerial: "BOARD-C"},
	})
	manager, err := New(Config{Discoverer: staticDiscoverer{result: discovery}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	port, status, err := manager.MatchCableSerial(context.Background(), "BOARD-A")
	if err != nil || status != "matched" || port.Name != "COM3" {
		t.Fatalf("BOARD-A match = %+v, %q, %v", port, status, err)
	}
	if _, status, err := manager.MatchCableSerial(context.Background(), "BOARD-B"); !errors.Is(err, ErrPortAmbiguous) || status != "ambiguous" {
		t.Fatalf("BOARD-B match status=%q error=%v", status, err)
	}
	if _, status, err := manager.MatchCableSerial(context.Background(), "BOARD-C"); !errors.Is(err, ErrPortNotFound) || status != "not_found" {
		t.Fatalf("BOARD-C match status=%q error=%v", status, err)
	}
}

func TestManagerReplaysRecentDataAndRejectsModeConflict(t *testing.T) {
	manager, port, handle := testManager(t)
	first, err := manager.Attach(context.Background(), AttachRequest{PortID: port.ID, BaudRate: 115200})
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	handle.reads <- []byte("early output")
	select {
	case <-first.Data():
	case <-time.After(time.Second):
		t.Fatal("first attachment received no data")
	}
	second, err := manager.Attach(context.Background(), AttachRequest{PortID: port.ID, BaudRate: 115200, Replay: true})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if got := <-second.Data(); string(got) != "early output" {
		t.Fatalf("replay = %q", got)
	}
	if _, err := manager.Attach(context.Background(), AttachRequest{PortID: port.ID, BaudRate: 9600}); !errors.Is(err, ErrModeConflict) {
		t.Fatalf("mode conflict error = %v", err)
	}
}

func TestManagerReportsDisconnect(t *testing.T) {
	manager, port, handle := testManager(t)
	attachment, err := manager.Attach(context.Background(), AttachRequest{PortID: port.ID})
	if err != nil {
		t.Fatal(err)
	}
	handle.Close()
	select {
	case <-attachment.Done():
		if attachment.Err() == nil {
			t.Fatal("disconnect error is nil")
		}
	case <-time.After(time.Second):
		t.Fatal("disconnect was not reported")
	}
}

func TestManagerKeepsMultipleDevicesIndependent(t *testing.T) {
	firstPort := newPort(candidate{
		Name: "/dev/ttyUSB1", VendorID: FTDIVendorID, ProductID: FT2232HProductID,
		USBSerial: "BOARD-A", Channel: FT2232HUARTChannel,
	})
	secondPort := newPort(candidate{
		Name: "/dev/ttyUSB3", VendorID: FTDIVendorID, ProductID: FT2232HProductID,
		USBSerial: "BOARD-B", Channel: FT2232HUARTChannel,
	})
	firstHandle := newFakePort()
	secondHandle := newFakePort()
	handles := map[string]*fakePort{firstPort.Name: firstHandle, secondPort.Name: secondHandle}
	manager, err := New(Config{
		Discoverer: staticDiscoverer{Discovery{Ports: []Port{firstPort, secondPort}}},
		Open:       func(name string, _ int) (PortHandle, error) { return handles[name], nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	first, err := manager.Attach(context.Background(), AttachRequest{PortID: firstPort.ID, Access: AccessController})
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := manager.Attach(context.Background(), AttachRequest{PortID: secondPort.ID, Access: AccessController})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	firstHandle.reads <- []byte("first")
	secondHandle.reads <- []byte("second")
	if got := <-first.Data(); string(got) != "first" {
		t.Fatalf("first device output = %q", got)
	}
	if got := <-second.Data(); string(got) != "second" {
		t.Fatalf("second device output = %q", got)
	}
}
