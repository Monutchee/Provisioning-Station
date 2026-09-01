// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

package httpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/Monutchee/Provisioning-Station/internal/serialconsole"
	"github.com/coder/websocket"
)

const (
	serialAttachTTL = 30 * time.Second
	serialAuthTTL   = 5 * time.Second
	maxSerialFrame  = 64 << 10
)

type serialSessionRequest struct {
	PortID   string               `json:"portId"`
	BaudRate int                  `json:"baudRate,omitempty"`
	Access   serialconsole.Access `json:"access"`
}

type serialSession struct {
	ID         string
	Token      string
	Attachment *serialconsole.Attachment
	ExpiresUTC time.Time
	Connected  bool
	Timer      *time.Timer
}

type serialSessions struct {
	mutex sync.Mutex
	items map[string]*serialSession
}

func newSerialSessions() *serialSessions {
	return &serialSessions{items: make(map[string]*serialSession)}
}

func (server *Server) serialPorts(response http.ResponseWriter, request *http.Request) {
	ctx, cancel := context.WithTimeout(request.Context(), 5*time.Second)
	defer cancel()
	result, err := server.serial.List(ctx)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "serial_discovery_failed", err.Error())
		return
	}
	writeJSON(response, http.StatusOK, result)
}

func (server *Server) createSerialSession(response http.ResponseWriter, request *http.Request) {
	var sessionRequest serialSessionRequest
	if err := decodeJSON(request, &sessionRequest); err != nil {
		writeError(response, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	attachment, err := server.serial.Attach(request.Context(), serialconsole.AttachRequest{
		PortID: sessionRequest.PortID, BaudRate: sessionRequest.BaudRate,
		Access: sessionRequest.Access, Replay: true,
	})
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, serialconsole.ErrControllerBusy) || errors.Is(err, serialconsole.ErrModeConflict) {
			status = http.StatusConflict
		} else if errors.Is(err, serialconsole.ErrPortNotFound) {
			status = http.StatusNotFound
		}
		writeError(response, status, "serial_session_rejected", err.Error())
		return
	}
	id, err := randomCredential(16)
	if err != nil {
		attachment.Close()
		writeError(response, http.StatusInternalServerError, "serial_session_failed", err.Error())
		return
	}
	token, err := randomCredential(32)
	if err != nil {
		attachment.Close()
		writeError(response, http.StatusInternalServerError, "serial_session_failed", err.Error())
		return
	}
	now := time.Now().UTC()
	session := &serialSession{
		ID: id, Token: token, Attachment: attachment, ExpiresUTC: now.Add(serialAttachTTL),
	}
	server.sessions.add(session)
	response.Header().Set("Cache-Control", "no-store")
	writeJSON(response, http.StatusCreated, map[string]any{
		"id": session.ID, "port": attachment.Port(), "baudRate": attachment.BaudRate(),
		"access": attachment.Access(), "attachToken": session.Token,
		"expiresUtc":    session.ExpiresUTC,
		"websocketPath": "/api/v1/serial/sessions/" + session.ID + "/stream",
	})
}

func (server *Server) deleteSerialSession(response http.ResponseWriter, request *http.Request) {
	session := server.sessions.remove(request.PathValue("id"))
	if session == nil {
		writeError(response, http.StatusNotFound, "serial_session_not_found", "serial session was not found")
		return
	}
	session.Attachment.Close()
	response.WriteHeader(http.StatusNoContent)
}

func (server *Server) jobSerialTranscript(response http.ResponseWriter, request *http.Request) {
	path, metadata, err := server.jobs.SerialTranscript(request.PathValue("id"))
	if err != nil {
		writeError(response, http.StatusNotFound, "serial_transcript_not_found", err.Error())
		return
	}
	file, err := os.Open(path)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "serial_transcript_failed", err.Error())
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		writeError(response, http.StatusInternalServerError, "serial_transcript_failed", err.Error())
		return
	}
	response.Header().Set("Content-Type", "application/octet-stream")
	response.Header().Set("Content-Disposition", `attachment; filename="`+serialTranscriptNameForDownload+`"`)
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("X-MNC-Serial-Capture-State", metadata.State)
	if metadata.Truncated {
		response.Header().Set("X-MNC-Serial-Truncated", "true")
	} else {
		response.Header().Set("X-MNC-Serial-Truncated", "false")
	}
	http.ServeContent(response, request, serialTranscriptNameForDownload, info.ModTime(), file)
}

const serialTranscriptNameForDownload = "serial-console.bin"

func (server *Server) streamSerialSession(response http.ResponseWriter, request *http.Request) {
	id := request.PathValue("id")
	if !server.sessions.pending(id) {
		writeError(response, http.StatusNotFound, "serial_session_not_found", "serial session was not found or has expired")
		return
	}
	connection, err := websocket.Accept(response, request, &websocket.AcceptOptions{
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		return
	}
	connection.SetReadLimit(maxSerialFrame)
	authContext, authCancel := context.WithTimeout(context.Background(), serialAuthTTL)
	messageType, data, readErr := connection.Read(authContext)
	authCancel()
	if readErr != nil || messageType != websocket.MessageText {
		_ = connection.Close(websocket.StatusPolicyViolation, "serial attach authentication required")
		if session := server.sessions.remove(id); session != nil {
			session.Attachment.Close()
		}
		return
	}
	var authMessage struct {
		Type  string `json:"type"`
		Token string `json:"token"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	decodeErr := decoder.Decode(&authMessage)
	var trailing any
	trailingErr := decoder.Decode(&trailing)
	if decodeErr != nil || trailingErr != io.EOF || authMessage.Type != "attach" {
		_ = connection.Close(websocket.StatusPolicyViolation, "invalid serial attach message")
		if session := server.sessions.remove(id); session != nil {
			session.Attachment.Close()
		}
		return
	}
	session := server.sessions.claim(id, authMessage.Token)
	if session == nil {
		_ = connection.Close(websocket.StatusPolicyViolation, "invalid or expired serial attach token")
		if expired := server.sessions.remove(id); expired != nil {
			expired.Attachment.Close()
		}
		return
	}
	defer func() {
		server.sessions.remove(id)
		session.Attachment.Close()
	}()

	streamContext, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 2)
	go func() { finished <- writeSerialWebSocket(streamContext, connection, session.Attachment) }()
	go func() { finished <- readSerialWebSocket(streamContext, connection, session.Attachment) }()
	streamErr := <-finished
	cancel()
	_ = connection.CloseNow()
	<-finished
	if streamErr == nil {
		return
	}
}

func writeSerialWebSocket(ctx context.Context, connection *websocket.Conn, attachment *serialconsole.Attachment) error {
	ready, err := json.Marshal(map[string]any{
		"type": "ready", "portId": attachment.Port().ID,
		"access": attachment.Access(), "baudRate": attachment.BaudRate(),
	})
	if err != nil {
		return err
	}
	if err := connection.Write(ctx, websocket.MessageText, ready); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case data, open := <-attachment.Data():
			if !open {
				return attachment.Err()
			}
			if err := connection.Write(ctx, websocket.MessageBinary, data); err != nil {
				return err
			}
		}
	}
}

func readSerialWebSocket(ctx context.Context, connection *websocket.Conn, attachment *serialconsole.Attachment) error {
	for {
		messageType, data, err := connection.Read(ctx)
		if err != nil {
			return err
		}
		if messageType != websocket.MessageBinary {
			return fmt.Errorf("serial stream accepts binary data frames only")
		}
		if attachment.Access() != serialconsole.AccessController {
			return fmt.Errorf("observer sessions cannot write to the serial console")
		}
		if err := attachment.Write(data); err != nil {
			return err
		}
	}
}

func (sessions *serialSessions) add(session *serialSession) {
	sessions.mutex.Lock()
	sessions.items[session.ID] = session
	session.Timer = time.AfterFunc(time.Until(session.ExpiresUTC), func() {
		if expired := sessions.expire(session.ID); expired != nil {
			expired.Attachment.Close()
		}
	})
	sessions.mutex.Unlock()
}

func (sessions *serialSessions) expire(id string) *serialSession {
	sessions.mutex.Lock()
	defer sessions.mutex.Unlock()
	session := sessions.items[id]
	if session == nil || session.Connected || time.Now().Before(session.ExpiresUTC) {
		return nil
	}
	delete(sessions.items, id)
	return session
}

func (sessions *serialSessions) pending(id string) bool {
	sessions.mutex.Lock()
	defer sessions.mutex.Unlock()
	session := sessions.items[id]
	return session != nil && !session.Connected && time.Now().Before(session.ExpiresUTC)
}

func (sessions *serialSessions) claim(id, token string) *serialSession {
	sessions.mutex.Lock()
	defer sessions.mutex.Unlock()
	session := sessions.items[id]
	if session == nil || session.Connected || !time.Now().Before(session.ExpiresUTC) ||
		len(token) != len(session.Token) || subtle.ConstantTimeCompare([]byte(token), []byte(session.Token)) != 1 {
		return nil
	}
	session.Connected = true
	session.Token = ""
	if session.Timer != nil {
		session.Timer.Stop()
	}
	return session
}

func (sessions *serialSessions) remove(id string) *serialSession {
	sessions.mutex.Lock()
	defer sessions.mutex.Unlock()
	session := sessions.items[id]
	if session != nil {
		delete(sessions.items, id)
		if session.Timer != nil {
			session.Timer.Stop()
		}
	}
	return session
}

func randomCredential(size int) (string, error) {
	data := make([]byte, size)
	if _, err := rand.Read(data); err != nil {
		return "", fmt.Errorf("generate serial session credential: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}
