package node

import (
	"sync"
	"time"

	"github.com/anycable/anycable-go/common"
	"github.com/apex/log"
	"github.com/gorilla/websocket"
)

const (
	// CloseNormalClosure indicates normal closure
	CloseNormalClosure = websocket.CloseNormalClosure

	// CloseInternalServerErr indicates closure because of internal error
	CloseInternalServerErr = websocket.CloseInternalServerErr

	// CloseAbnormalClosure indicates abnormal close
	CloseAbnormalClosure = websocket.CloseAbnormalClosure

	// CloseGoingAway indicates closing because of server shuts down or client disconnects
	CloseGoingAway = websocket.CloseGoingAway

	writeWait    = 10 * time.Second
	pingInterval = 3 * time.Second
)

var (
	expectedCloseStatuses = []int{
		websocket.CloseNormalClosure,    // Reserved in case ActionCable fixes its behaviour
		websocket.CloseGoingAway,        // Web browser page was closed
		websocket.CloseNoStatusReceived, // ActionCable don't care about closing
	}
)

type frameType int

const (
	textFrame  frameType = 0
	closeFrame frameType = 1
)

type sentFrame struct {
	frameType   frameType
	payload     []byte
	closeCode   int
	closeReason string
}

// Session represents active client
type Session struct {
	node          *Node
	ws            Connection
	env           *common.SessionEnv
	subscriptions map[string]bool
	closed        bool
	connected     bool
	// Main mutex (for read/write and important session updates)
	mu sync.Mutex
	// Mutex for protocol-related state (env, subscriptions)
	smu       sync.Mutex
	pingTimer *time.Timer

	UID         string
	Identifiers string
	Log         *log.Entry
}

// NewSession build a new Session struct from ws connetion and http request
func NewSession(node *Node, ws Connection, url string, headers map[string]string, uid string) *Session {
	session := &Session{
		node:          node,
		ws:            ws,
		env:           common.NewSessionEnv(url, &headers),
		subscriptions: make(map[string]bool),
		closed:        false,
		connected:     false,
	}

	session.UID = uid

	ctx := node.log.WithFields(log.Fields{
		"sid": session.UID,
	})

	session.Log = ctx

	session.addPing()

	return session
}

// ReadMessage reads messages from ws connection and send them to node
func (s *Session) ReadMessage() {
	message, err := s.ws.Read()

	if err != nil {
		if websocket.IsCloseError(err, expectedCloseStatuses...) {
			s.Log.Debugf("Websocket closed: %v", err)
			s.Disconnect("Read closed", CloseNormalClosure)
		} else {
			s.Log.Debugf("Websocket close error: %v", err)
			s.Disconnect("Read failed", CloseAbnormalClosure)
		}
		return
	}

	if err := s.node.HandleCommand(s, message); err != nil {
		s.Log.Warnf("Failed to handle incoming message '%s' with error: %v", message, err)
	}
}

// Send schedules a data transmission
func (s *Session) Send(msg []byte) {
	s.Schedule(func() {
		s.SendImmediately(msg)
	})
}

// SendImmediately sends a message right away without adding to a queue
func (s *Session) SendImmediately(msg []byte) {
	s.sendFrame(&sentFrame{frameType: textFrame, payload: msg})
}

// Disconnect schedules connection disconnect
func (s *Session) Disconnect(reason string, code int) {
	s.Schedule(func() {
		s.DisconnectImmediately(reason, code)
	})
}

// DisconnectImmediately enqueues RPC disconnect request and closes the connection right away
func (s *Session) DisconnectImmediately(reason string, code int) {
	s.mu.Lock()
	if s.connected {
		defer s.node.Disconnect(s) // nolint:errcheck
	}
	s.connected = false
	s.mu.Unlock()

	s.close(reason, code)
}

// Schedule adds a taks to the execution queue
func (s *Session) Schedule(task func()) {
	task()
}

// Flush executes tasks from the queue
func (s *Session) Flush() {

}

func (s *Session) close(reason string, code int) {
	s.mu.Lock()

	if s.closed {
		s.mu.Unlock()
		return
	}

	s.closed = true
	s.mu.Unlock()

	s.sendClose(reason, code)

	if s.pingTimer != nil {
		s.pingTimer.Stop()
	}
}

func (s *Session) sendClose(reason string, code int) {
	s.sendFrame(&sentFrame{
		frameType:   closeFrame,
		closeReason: reason,
		closeCode:   code,
	})
}

func (s *Session) sendFrame(message *sentFrame) {
	switch message.frameType {
	case textFrame:
		err := s.write(message.payload, time.Now().Add(writeWait))

		if err != nil {
			return
		}
	case closeFrame:
		s.ws.Close(message.closeCode, message.closeReason)
		return
	default:
		s.Log.Errorf("Unknown frame type: %v", message)
		return
	}
}

func (s *Session) write(message []byte, deadline time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.ws.Write(message, deadline)
}

func (s *Session) sendPing() {
	if s.closed {
		return
	}

	deadline := time.Now().Add(pingInterval / 2)
	err := s.write(newPingMessage(), deadline)

	if err == nil {
		s.addPing()
	} else {
		s.Disconnect("Ping failed", CloseAbnormalClosure)
	}
}

func (s *Session) addPing() {
	s.pingTimer = time.AfterFunc(pingInterval, s.sendPing)
}

func newPingMessage() []byte {
	return (&common.PingMessage{Type: "ping", Message: time.Now().Unix()}).ToJSON()
}
