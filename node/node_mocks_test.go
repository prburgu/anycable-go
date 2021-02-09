package node

import (
	"errors"
	"time"

	"github.com/anycable/anycable-go/common"
	"github.com/anycable/anycable-go/metrics"
	"github.com/anycable/anycable-go/mocks"
	"github.com/anycable/anycable-go/utils"

	"github.com/apex/log"
)

// NewMockNode build new node with mock controller
func NewMockNode() Node {
	controller := mocks.NewMockController()
	node := NewNode(&controller, metrics.NewMetrics(nil, 10))
	node.GoPool, _ = utils.NewGoPool(5, 1, 2)
	config := NewDisconnectQueueConfig()
	config.Rate = 1
	node.SetDisconnector(NewDisconnectQueue(node, &config))
	return *node
}

type MockConnection struct {
	send   chan []byte
	closed bool
}

func (conn MockConnection) Write(msg []byte, deadline time.Time) error {
	conn.send <- msg
	return nil
}

func (conn MockConnection) Read() ([]byte, error) {
	timer := time.After(100 * time.Millisecond)

	select {
	case <-timer:
		return nil, errors.New("Session hasn't received any messages")
	case msg := <-conn.send:
		return msg, nil
	}
}

func (conn MockConnection) Close(_code int, _reason string) {
	conn.closed = true
}

func NewMockConnection() MockConnection {
	return MockConnection{closed: false, send: make(chan []byte, 2)}
}

// NewMockSession returns a new session with a specified uid and identifiers equal to uid
func NewMockSession(uid string, node *Node) *Session {
	return &Session{
		node:          node,
		ws:            NewMockConnection(),
		closed:        true,
		UID:           uid,
		Identifiers:   uid,
		Log:           log.WithField("sid", uid),
		subscriptions: make(map[string]bool),
		env:           common.NewSessionEnv("/cable-test", &map[string]string{}),
	}
}

// NewMockSession returns a new session with a specified uid, path and headers, and identifiers equal to uid
func NewMockSessionWithEnv(uid string, node *Node, url string, headers *map[string]string) *Session {
	session := NewMockSession(uid, node)
	session.env = common.NewSessionEnv(url, headers)
	return session
}
