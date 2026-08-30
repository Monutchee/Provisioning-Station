// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

// Package tftp implements the read-only subset of TFTP required for Station
// boot artifacts. Each request receives its own transfer ID, as required by
// RFC 1350, and supports the option negotiation used by U-Boot.
package tftp

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	opcodeRRQ   = 1
	opcodeDATA  = 3
	opcodeACK   = 4
	opcodeERROR = 5
	opcodeOACK  = 6

	errorNotFound        = 1
	errorAccessViolation = 2
	errorIllegalOp       = 4
	errorOption          = 8
)

type Config struct {
	ListenAddress   string
	Root            string
	AllowedClientIP string
	Retries         int
	Timeout         time.Duration
	MaxBlockSize    int
	EventBuffer     int
}

func DefaultConfig(root string) Config {
	return Config{
		ListenAddress: ":69",
		Root:          root,
		Retries:       5,
		Timeout:       3 * time.Second,
		MaxBlockSize:  1468,
		EventBuffer:   64,
	}
}

type TransferEvent struct {
	Time     time.Time `json:"time"`
	Filename string    `json:"filename"`
	Remote   string    `json:"remote"`
	Bytes    int64     `json:"bytes"`
	Status   string    `json:"status"`
	Error    string    `json:"error,omitempty"`
}

type Server struct {
	config        Config
	root          string
	conn          *net.UDPConn
	allowedClient net.IP
	events        chan TransferEvent

	closeOnce sync.Once
	wait      sync.WaitGroup
	activeMu  sync.Mutex
	active    map[string]struct{}
}

func Listen(config Config) (*Server, error) {
	if config.ListenAddress == "" {
		return nil, fmt.Errorf("TFTP listen address must not be empty")
	}
	if config.Retries < 1 {
		return nil, fmt.Errorf("TFTP retries must be positive")
	}
	if config.Timeout <= 0 {
		return nil, fmt.Errorf("TFTP timeout must be positive")
	}
	if config.MaxBlockSize < 8 || config.MaxBlockSize > 65464 {
		return nil, fmt.Errorf("TFTP maximum block size must be between 8 and 65464")
	}
	if config.EventBuffer < 1 {
		config.EventBuffer = 64
	}
	root, err := filepath.Abs(config.Root)
	if err != nil {
		return nil, fmt.Errorf("resolve TFTP root: %w", err)
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("TFTP root is not a directory: %s", root)
	}
	var allowedClient net.IP
	if config.AllowedClientIP != "" {
		allowedClient = net.ParseIP(config.AllowedClientIP)
		if allowedClient == nil || allowedClient.To4() == nil {
			return nil, fmt.Errorf("TFTP allowed client is not an IPv4 address: %q", config.AllowedClientIP)
		}
		allowedClient = allowedClient.To4()
	}
	address, err := net.ResolveUDPAddr("udp4", config.ListenAddress)
	if err != nil {
		return nil, fmt.Errorf("resolve TFTP listen address: %w", err)
	}
	connection, err := net.ListenUDP("udp4", address)
	if err != nil {
		return nil, fmt.Errorf("listen for TFTP on %s: %w", config.ListenAddress, err)
	}
	return &Server{
		config:        config,
		root:          root,
		conn:          connection,
		allowedClient: allowedClient,
		events:        make(chan TransferEvent, config.EventBuffer),
		active:        make(map[string]struct{}),
	}, nil
}

func (server *Server) Addr() net.Addr {
	return server.conn.LocalAddr()
}

func (server *Server) Events() <-chan TransferEvent {
	return server.events
}

func (server *Server) Serve(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		server.Close()
	}()

	buffer := make([]byte, 65535)
	for {
		count, remote, err := server.conn.ReadFromUDP(buffer)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				server.wait.Wait()
				return nil
			}
			return fmt.Errorf("read TFTP request: %w", err)
		}
		packet := append([]byte(nil), buffer[:count]...)
		if server.allowedClient != nil && !remote.IP.Equal(server.allowedClient) {
			server.sendControlError(remote, errorAccessViolation, "TFTP client IP is not allowed")
			continue
		}
		request, err := parseRequest(packet)
		if err != nil {
			server.sendControlError(remote, errorIllegalOp, err.Error())
			continue
		}
		key := remote.String() + "\x00" + request.filename
		server.activeMu.Lock()
		_, duplicate := server.active[key]
		if !duplicate {
			server.active[key] = struct{}{}
		}
		server.activeMu.Unlock()
		if duplicate {
			continue
		}

		server.wait.Add(1)
		go func() {
			defer server.wait.Done()
			defer func() {
				server.activeMu.Lock()
				delete(server.active, key)
				server.activeMu.Unlock()
			}()
			server.serveRequest(ctx, remote, request)
		}()
	}
}

func (server *Server) Close() error {
	var err error
	server.closeOnce.Do(func() {
		err = server.conn.Close()
	})
	return err
}

type request struct {
	filename string
	options  map[string]string
}

func parseRequest(packet []byte) (request, error) {
	if len(packet) < 4 || binary.BigEndian.Uint16(packet[:2]) != opcodeRRQ {
		return request{}, fmt.Errorf("only read requests are supported")
	}
	parts := splitZeroTerminated(packet[2:])
	if len(parts) < 2 || len(parts)%2 != 0 {
		return request{}, fmt.Errorf("malformed TFTP read request")
	}
	filename := parts[0]
	if err := validateFilename(filename); err != nil {
		return request{}, err
	}
	if !strings.EqualFold(parts[1], "octet") {
		return request{}, fmt.Errorf("only octet transfer mode is supported")
	}
	options := make(map[string]string)
	for index := 2; index < len(parts); index += 2 {
		name := strings.ToLower(parts[index])
		if name == "" {
			return request{}, fmt.Errorf("empty TFTP option name")
		}
		if _, duplicate := options[name]; duplicate {
			return request{}, fmt.Errorf("duplicate TFTP option %q", name)
		}
		options[name] = parts[index+1]
	}
	return request{filename: filename, options: options}, nil
}

func splitZeroTerminated(data []byte) []string {
	if len(data) == 0 || data[len(data)-1] != 0 {
		return nil
	}
	fields := strings.Split(string(data[:len(data)-1]), "\x00")
	for _, field := range fields {
		if field == "" {
			return nil
		}
	}
	return fields
}

func validateFilename(name string) error {
	if name == "" || len(name) > 1024 || strings.HasPrefix(name, "/") || strings.ContainsAny(name, `\:`) {
		return fmt.Errorf("unsafe TFTP filename %q", name)
	}
	for _, character := range name {
		if character < 0x20 || character == 0x7f {
			return fmt.Errorf("unsafe TFTP filename %q", name)
		}
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("unsafe TFTP filename %q", name)
		}
	}
	return nil
}

func (server *Server) serveRequest(ctx context.Context, remote *net.UDPAddr, request request) {
	event := TransferEvent{
		Time:     time.Now().UTC(),
		Filename: request.filename,
		Remote:   remote.String(),
		Status:   "started",
	}
	server.emit(event)

	path := filepath.Join(server.root, filepath.FromSlash(request.filename))
	info, err := os.Lstat(path)
	if err != nil {
		server.transferFailure(remote, request.filename, errorNotFound, "file not found", err)
		return
	}
	if !info.Mode().IsRegular() {
		server.transferFailure(remote, request.filename, errorAccessViolation, "requested path is not a regular file", nil)
		return
	}
	file, err := os.Open(path)
	if err != nil {
		server.transferFailure(remote, request.filename, errorAccessViolation, "cannot open requested file", err)
		return
	}
	defer file.Close()

	connection, err := listenTransferSocket(remote)
	if err != nil {
		server.emitFailure(request.filename, remote.String(), 0, err)
		return
	}
	defer connection.Close()
	stopCancel := context.AfterFunc(ctx, func() { _ = connection.Close() })
	defer stopCancel()

	negotiated, blockSize, timeout, err := server.negotiate(request.options, info.Size())
	if err != nil {
		sendError(connection, remote, errorOption, err.Error())
		server.emitFailure(request.filename, remote.String(), 0, err)
		return
	}
	if len(negotiated) > 0 {
		packet := optionAckPacket(negotiated)
		if err := exchange(ctx, connection, remote, packet, 0, server.config.Retries, timeout); err != nil {
			server.emitFailure(request.filename, remote.String(), 0, err)
			return
		}
	}

	buffer := make([]byte, blockSize)
	var transferred int64
	block := uint16(1)
	for {
		if err := ctx.Err(); err != nil {
			server.emitFailure(request.filename, remote.String(), transferred, err)
			return
		}
		count, readErr := io.ReadFull(file, buffer)
		if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
			server.emitFailure(request.filename, remote.String(), transferred, readErr)
			return
		}
		packet := dataPacket(block, buffer[:count])
		if err := exchange(ctx, connection, remote, packet, block, server.config.Retries, timeout); err != nil {
			server.emitFailure(request.filename, remote.String(), transferred, err)
			return
		}
		transferred += int64(count)
		if count < blockSize {
			server.emit(TransferEvent{
				Time:     time.Now().UTC(),
				Filename: request.filename,
				Remote:   remote.String(),
				Bytes:    transferred,
				Status:   "completed",
			})
			return
		}
		block++
	}
}

func (server *Server) negotiate(options map[string]string, fileSize int64) ([][2]string, int, time.Duration, error) {
	blockSize := 512
	timeout := server.config.Timeout
	accepted := make([][2]string, 0, 3)
	if value, ok := options["blksize"]; ok {
		requested, err := strconv.Atoi(value)
		if err != nil || requested < 8 || requested > 65464 {
			return nil, 0, 0, fmt.Errorf("invalid blksize option %q", value)
		}
		if requested > server.config.MaxBlockSize {
			requested = server.config.MaxBlockSize
		}
		blockSize = requested
		accepted = append(accepted, [2]string{"blksize", strconv.Itoa(blockSize)})
	}
	if value, ok := options["timeout"]; ok {
		seconds, err := strconv.Atoi(value)
		if err != nil || seconds < 1 || seconds > 255 {
			return nil, 0, 0, fmt.Errorf("invalid timeout option %q", value)
		}
		timeout = time.Duration(seconds) * time.Second
		accepted = append(accepted, [2]string{"timeout", strconv.Itoa(seconds)})
	}
	if value, ok := options["tsize"]; ok {
		if value != "0" {
			return nil, 0, 0, fmt.Errorf("read-request tsize must be zero")
		}
		accepted = append(accepted, [2]string{"tsize", strconv.FormatInt(fileSize, 10)})
	}
	return accepted, blockSize, timeout, nil
}

func exchange(
	ctx context.Context,
	connection *net.UDPConn,
	remote *net.UDPAddr,
	packet []byte,
	expectedBlock uint16,
	retries int,
	timeout time.Duration,
) error {
	response := make([]byte, 65535)
	for attempt := 0; attempt < retries; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := connection.WriteToUDP(packet, remote); err != nil {
			return fmt.Errorf("send TFTP packet: %w", err)
		}
		deadline := time.Now().Add(timeout)
		for {
			if err := connection.SetReadDeadline(deadline); err != nil {
				return err
			}
			count, sender, err := connection.ReadFromUDP(response)
			if err != nil {
				var networkError net.Error
				if errors.As(err, &networkError) && networkError.Timeout() {
					break
				}
				return fmt.Errorf("receive TFTP acknowledgement: %w", err)
			}
			if !sender.IP.Equal(remote.IP) || sender.Port != remote.Port {
				continue
			}
			if count >= 4 && binary.BigEndian.Uint16(response[:2]) == opcodeACK &&
				binary.BigEndian.Uint16(response[2:4]) == expectedBlock {
				return nil
			}
			if count >= 4 && binary.BigEndian.Uint16(response[:2]) == opcodeERROR {
				return fmt.Errorf("TFTP client returned error %d: %s", binary.BigEndian.Uint16(response[2:4]), packetMessage(response[:count]))
			}
		}
	}
	return fmt.Errorf("TFTP acknowledgement timed out after %d attempts", retries)
}

func dataPacket(block uint16, data []byte) []byte {
	packet := make([]byte, 4+len(data))
	binary.BigEndian.PutUint16(packet[:2], opcodeDATA)
	binary.BigEndian.PutUint16(packet[2:4], block)
	copy(packet[4:], data)
	return packet
}

func optionAckPacket(options [][2]string) []byte {
	packet := []byte{0, opcodeOACK}
	for _, option := range options {
		packet = append(packet, option[0]...)
		packet = append(packet, 0)
		packet = append(packet, option[1]...)
		packet = append(packet, 0)
	}
	return packet
}

func errorPacket(code uint16, message string) []byte {
	message = strings.ReplaceAll(message, "\x00", "")
	packet := make([]byte, 4, 5+len(message))
	binary.BigEndian.PutUint16(packet[:2], opcodeERROR)
	binary.BigEndian.PutUint16(packet[2:4], code)
	packet = append(packet, message...)
	packet = append(packet, 0)
	return packet
}

func sendError(connection *net.UDPConn, remote *net.UDPAddr, code uint16, message string) {
	_, _ = connection.WriteToUDP(errorPacket(code, message), remote)
}

func (server *Server) sendControlError(remote *net.UDPAddr, code uint16, message string) {
	_, _ = server.conn.WriteToUDP(errorPacket(code, message), remote)
}

func (server *Server) transferFailure(remote *net.UDPAddr, filename string, code uint16, message string, cause error) {
	connection, err := listenTransferSocket(remote)
	if err == nil {
		sendError(connection, remote, code, message)
		connection.Close()
	}
	if cause == nil {
		cause = errors.New(message)
	}
	server.emitFailure(filename, remote.String(), 0, cause)
}

func listenTransferSocket(remote *net.UDPAddr) (*net.UDPConn, error) {
	network := "udp6"
	if remote.IP.To4() != nil {
		network = "udp4"
	}
	return net.ListenUDP(network, &net.UDPAddr{Port: 0})
}

func (server *Server) emitFailure(filename, remote string, bytes int64, err error) {
	server.emit(TransferEvent{
		Time:     time.Now().UTC(),
		Filename: filename,
		Remote:   remote,
		Bytes:    bytes,
		Status:   "failed",
		Error:    err.Error(),
	})
}

func (server *Server) emit(event TransferEvent) {
	server.events <- event
}

func packetMessage(packet []byte) string {
	if len(packet) <= 4 {
		return ""
	}
	message := packet[4:]
	if index := strings.IndexByte(string(message), 0); index >= 0 {
		message = message[:index]
	}
	return string(message)
}
