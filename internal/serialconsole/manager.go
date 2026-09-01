// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

package serialconsole

import (
	"context"
	"fmt"
	"io"
	"sync"

	"go.bug.st/serial"
)

type Access string

const (
	AccessObserver   Access = "observer"
	AccessController Access = "controller"
)

type PortHandle interface {
	io.Reader
	io.Writer
	io.Closer
}

type OpenFunc func(string, int) (PortHandle, error)

type Config struct {
	Discoverer  Discoverer
	Open        OpenFunc
	DefaultBaud int
	ReplayBytes int
}

type Manager struct {
	discoverer  Discoverer
	open        OpenFunc
	defaultBaud int
	replayBytes int

	mutex       sync.Mutex
	connections map[string]*connection
	nextID      uint64
	closed      bool
}

type connection struct {
	port       Port
	baudRate   int
	handle     PortHandle
	writeMutex sync.Mutex
	ring       []byte
	clients    map[uint64]*subscriber
	controller uint64
}

type subscriber struct {
	id     uint64
	data   chan []byte
	done   chan struct{}
	err    error
	closed bool
}

type AttachRequest struct {
	PortID   string
	BaudRate int
	Access   Access
	Replay   bool
}

type Attachment struct {
	manager  *Manager
	portID   string
	id       uint64
	client   *subscriber
	port     Port
	baudRate int
	access   Access
	data     <-chan []byte
	done     <-chan struct{}
}

func New(config Config) (*Manager, error) {
	if config.Discoverer == nil {
		config.Discoverer = NativeDiscoverer{}
	}
	if config.Open == nil {
		config.Open = openNativePort
	}
	if config.DefaultBaud == 0 {
		config.DefaultBaud = DefaultBaudRate
	}
	if err := ValidateBaudRate(config.DefaultBaud); err != nil {
		return nil, err
	}
	if config.ReplayBytes == 0 {
		config.ReplayBytes = DefaultReplayBytes
	}
	if config.ReplayBytes < 0 || config.ReplayBytes > 16<<20 {
		return nil, fmt.Errorf("serial replay size must be between 0 and 16777216 bytes")
	}
	return &Manager{
		discoverer: config.Discoverer, open: config.Open,
		defaultBaud: config.DefaultBaud, replayBytes: config.ReplayBytes,
		connections: make(map[string]*connection),
	}, nil
}

func openNativePort(name string, baudRate int) (PortHandle, error) {
	return serial.Open(name, &serial.Mode{
		BaudRate: baudRate, DataBits: 8, Parity: serial.NoParity, StopBits: serial.OneStopBit,
		InitialStatusBits: &serial.ModemOutputBits{},
	})
}

func (manager *Manager) DefaultBaudRate() int {
	return manager.defaultBaud
}

func (manager *Manager) ReplayLimit() int {
	return manager.replayBytes
}

func (manager *Manager) List(ctx context.Context) (Discovery, error) {
	result, err := manager.discoverer.Discover(ctx)
	if err != nil {
		return Discovery{}, err
	}
	manager.mutex.Lock()
	defer manager.mutex.Unlock()
	for index := range result.Ports {
		if current := manager.connections[result.Ports[index].ID]; current != nil {
			result.Ports[index].Busy = true
			result.Ports[index].HasController = current.controller != 0
			result.Ports[index].ActiveBaudRate = current.baudRate
		}
	}
	return result, nil
}

func (manager *Manager) MatchCableSerial(ctx context.Context, serialNumber string) (Port, string, error) {
	result, err := manager.List(ctx)
	if err != nil {
		return Port{}, "not_found", err
	}
	for _, warning := range result.Warnings {
		if warning.Code == "serial_duplicate" && warning.USBSerial == serialNumber {
			return Port{}, "ambiguous", ErrPortAmbiguous
		}
	}
	for _, port := range result.Ports {
		if automaticallyMatchable(port) && port.USBSerial == serialNumber {
			return port, "matched", nil
		}
	}
	return Port{}, "not_found", ErrPortNotFound
}

func (manager *Manager) Attach(ctx context.Context, request AttachRequest) (*Attachment, error) {
	if request.PortID == "" {
		return nil, fmt.Errorf("serial portId must not be empty")
	}
	if request.BaudRate == 0 {
		request.BaudRate = manager.defaultBaud
	}
	if err := ValidateBaudRate(request.BaudRate); err != nil {
		return nil, err
	}
	if request.Access == "" {
		request.Access = AccessObserver
	}
	if request.Access != AccessObserver && request.Access != AccessController {
		return nil, fmt.Errorf("serial access must be %q or %q", AccessObserver, AccessController)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	discovery, err := manager.discoverer.Discover(ctx)
	if err != nil {
		return nil, fmt.Errorf("discover serial console: %w", err)
	}
	var selected *Port
	for index := range discovery.Ports {
		if discovery.Ports[index].ID == request.PortID {
			selected = &discovery.Ports[index]
			break
		}
	}
	if selected == nil {
		return nil, ErrPortNotFound
	}

	manager.mutex.Lock()
	if manager.closed {
		manager.mutex.Unlock()
		return nil, fmt.Errorf("serial console manager is closed")
	}
	current := manager.connections[selected.ID]
	created := false
	if current == nil {
		handle, openErr := manager.open(selected.Name, request.BaudRate)
		if openErr != nil {
			manager.mutex.Unlock()
			return nil, fmt.Errorf("open serial console %s: %w", selected.Name, openErr)
		}
		current = &connection{
			port: *selected, baudRate: request.BaudRate, handle: handle,
			clients: make(map[uint64]*subscriber), ring: make([]byte, 0, manager.replayBytes),
		}
		manager.connections[selected.ID] = current
		created = true
	} else if current.baudRate != request.BaudRate {
		manager.mutex.Unlock()
		return nil, fmt.Errorf("%w: open at %d baud", ErrModeConflict, current.baudRate)
	}
	if request.Access == AccessController && current.controller != 0 {
		if created {
			delete(manager.connections, selected.ID)
		}
		manager.mutex.Unlock()
		if created {
			_ = current.handle.Close()
		}
		return nil, ErrControllerBusy
	}
	manager.nextID++
	id := manager.nextID
	subscription := &subscriber{id: id, data: make(chan []byte, 128), done: make(chan struct{})}
	current.clients[id] = subscription
	if request.Access == AccessController {
		current.controller = id
	}
	if request.Replay && len(current.ring) != 0 {
		replay := append([]byte(nil), current.ring...)
		subscription.data <- replay
	}
	attachmentPort := *selected
	attachmentPort.Busy = true
	attachmentPort.HasController = current.controller != 0
	attachmentPort.ActiveBaudRate = current.baudRate
	attachment := &Attachment{
		manager: manager, portID: selected.ID, id: id, client: subscription, port: attachmentPort,
		baudRate: request.BaudRate, access: request.Access,
		data: subscription.data, done: subscription.done,
	}
	manager.mutex.Unlock()
	if created {
		go manager.readLoop(current)
	}
	return attachment, nil
}

func (manager *Manager) readLoop(current *connection) {
	buffer := make([]byte, 4096)
	for {
		read, err := current.handle.Read(buffer)
		if read > 0 {
			manager.broadcast(current, buffer[:read])
		}
		if err != nil {
			manager.endConnection(current, fmt.Errorf("serial console disconnected: %w", err))
			return
		}
		if read == 0 {
			manager.endConnection(current, fmt.Errorf("serial console disconnected: %w", io.EOF))
			return
		}
	}
}

func (manager *Manager) broadcast(current *connection, data []byte) {
	manager.mutex.Lock()
	if manager.connections[current.port.ID] != current {
		manager.mutex.Unlock()
		return
	}
	if manager.replayBytes != 0 {
		current.ring = append(current.ring, data...)
		if len(current.ring) > manager.replayBytes {
			current.ring = append(current.ring[:0], current.ring[len(current.ring)-manager.replayBytes:]...)
		}
	}
	for id, subscription := range current.clients {
		chunk := append([]byte(nil), data...)
		select {
		case subscription.data <- chunk:
		default:
			manager.closeSubscriberLocked(current, id, ErrSlowConsumer)
		}
	}
	shouldClose := len(current.clients) == 0
	if shouldClose {
		delete(manager.connections, current.port.ID)
	}
	manager.mutex.Unlock()
	if shouldClose {
		_ = current.handle.Close()
	}
}

func (manager *Manager) endConnection(current *connection, err error) {
	manager.mutex.Lock()
	if manager.connections[current.port.ID] == current {
		delete(manager.connections, current.port.ID)
		for id := range current.clients {
			manager.closeSubscriberLocked(current, id, err)
		}
	}
	manager.mutex.Unlock()
	_ = current.handle.Close()
}

func (manager *Manager) closeSubscriberLocked(current *connection, id uint64, err error) {
	subscription := current.clients[id]
	if subscription == nil || subscription.closed {
		return
	}
	subscription.closed = true
	subscription.err = err
	delete(current.clients, id)
	if current.controller == id {
		current.controller = 0
	}
	close(subscription.data)
	close(subscription.done)
}

func (manager *Manager) detach(portID string, id uint64) {
	manager.mutex.Lock()
	current := manager.connections[portID]
	if current == nil {
		manager.mutex.Unlock()
		return
	}
	manager.closeSubscriberLocked(current, id, nil)
	shouldClose := len(current.clients) == 0
	if shouldClose {
		delete(manager.connections, portID)
	}
	manager.mutex.Unlock()
	if shouldClose {
		_ = current.handle.Close()
	}
}

func (manager *Manager) write(portID string, id uint64, data []byte) error {
	manager.mutex.Lock()
	current := manager.connections[portID]
	if current == nil || current.controller != id {
		manager.mutex.Unlock()
		return fmt.Errorf("serial console controller lease is not held")
	}
	manager.mutex.Unlock()
	current.writeMutex.Lock()
	defer current.writeMutex.Unlock()
	for len(data) != 0 {
		written, err := current.handle.Write(data)
		if err != nil {
			return fmt.Errorf("write serial console: %w", err)
		}
		if written <= 0 {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}

func (manager *Manager) Close() {
	manager.mutex.Lock()
	if manager.closed {
		manager.mutex.Unlock()
		return
	}
	manager.closed = true
	connections := make([]*connection, 0, len(manager.connections))
	for _, current := range manager.connections {
		connections = append(connections, current)
		for id := range current.clients {
			manager.closeSubscriberLocked(current, id, fmt.Errorf("serial console manager stopped"))
		}
	}
	manager.connections = make(map[string]*connection)
	manager.mutex.Unlock()
	for _, current := range connections {
		_ = current.handle.Close()
	}
}

func (attachment *Attachment) Port() Port            { return attachment.port }
func (attachment *Attachment) BaudRate() int         { return attachment.baudRate }
func (attachment *Attachment) Access() Access        { return attachment.access }
func (attachment *Attachment) Data() <-chan []byte   { return attachment.data }
func (attachment *Attachment) Done() <-chan struct{} { return attachment.done }
func (attachment *Attachment) Close()                { attachment.manager.detach(attachment.portID, attachment.id) }
func (attachment *Attachment) Write(data []byte) error {
	return attachment.manager.write(attachment.portID, attachment.id, data)
}

func (attachment *Attachment) Err() error {
	attachment.manager.mutex.Lock()
	defer attachment.manager.mutex.Unlock()
	return attachment.client.err
}
