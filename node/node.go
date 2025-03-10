package node

import (
	"encoding/json"
	"fmt"
	"runtime"
	"time"

	"github.com/anycable/anycable-go/common"
	"github.com/anycable/anycable-go/metrics"
	"github.com/apex/log"
)

const (
	// serverRestartReason is the disconnect reason on shutdown
	serverRestartReason    = "server_restart"
	remoteDisconnectReason = "remote"

	metricsGoroutines      = "goroutines_num"
	metricsMemSys          = "mem_sys_bytes"
	metricsClientsNum      = "clients_num"
	metricsUniqClientsNum  = "clients_uniq_num"
	metricsStreamsNum      = "broadcast_streams_num"
	metricsDisconnectQueue = "disconnect_queue_size"

	metricsFailedAuths      = "failed_auths_total"
	metricsReceivedMsg      = "client_msg_total"
	metricsUnknownReceived  = "failed_client_msg_total"
	metricsBroadcastMsg     = "broadcast_msg_total"
	metricsUnknownBroadcast = "failed_broadcast_msg_total"
)

// AppNode describes a basic node interface
type AppNode interface {
	HandlePubSub(msg []byte)
}

// Node represents the whole application
type Node struct {
	Metrics *metrics.Metrics

	config       *Config
	hub          *Hub
	controller   Controller
	disconnector Disconnector
	shutdownCh   chan struct{}
	log          *log.Entry
}

// NewNode builds new node struct
func NewNode(controller Controller, metrics *metrics.Metrics, config *Config) *Node {
	node := &Node{
		Metrics:    metrics,
		config:     config,
		controller: controller,
		shutdownCh: make(chan struct{}),
		log:        log.WithFields(log.Fields{"context": "node"}),
	}

	node.hub = NewHub()

	node.registerMetrics()

	return node
}

// Start runs all the required goroutines
func (n *Node) Start() {
	go n.hub.Run()
	go n.collectStats()
}

// SetDisconnector set disconnector for the node
func (n *Node) SetDisconnector(d Disconnector) {
	n.disconnector = d
}

// HandleCommand parses incoming message from client and
// execute the command (if recognized)
func (n *Node) HandleCommand(s *Session, raw []byte) error {
	msg := &common.Message{}

	n.Metrics.Counter(metricsReceivedMsg).Inc()

	if err := json.Unmarshal(raw, &msg); err != nil {
		n.Metrics.Counter(metricsUnknownReceived).Inc()
		return err
	}

	s.Log.Debugf("Incoming message: %s", msg)
	switch msg.Command {
	case "subscribe":
		return n.Subscribe(s, msg)
	case "unsubscribe":
		return n.Unsubscribe(s, msg)
	case "message":
		return n.Perform(s, msg)
	default:
		n.Metrics.Counter(metricsUnknownReceived).Inc()
		return fmt.Errorf("Unknown command: %s", msg.Command)
	}
}

// HandlePubSub parses incoming pubsub message and broadcast it
func (n *Node) HandlePubSub(raw []byte) {
	msg, err := common.PubSubMessageFromJSON(raw)

	if err != nil {
		n.Metrics.Counter(metricsUnknownBroadcast).Inc()
		n.log.Warnf("Failed to parse pubsub message '%s' with error: %v", raw, err)
		return
	}

	switch v := msg.(type) {
	case common.StreamMessage:
		n.Broadcast(&v)
	case common.RemoteDisconnectMessage:
		n.RemoteDisconnect(&v)
	}
}

// Shutdown stops all services (hub, controller)
func (n *Node) Shutdown() {
	close(n.shutdownCh)

	if n.hub != nil {
		n.hub.Shutdown()

		active := len(n.hub.sessions)

		if active > 0 {
			n.log.Infof("Closing active connections: %d", active)
			disconnectMessage := newDisconnectMessage(serverRestartReason, true)
			// Close all registered sessions
			for _, session := range n.hub.sessions {
				session.Send(disconnectMessage)
				session.Disconnect("Shutdown", CloseGoingAway)
			}

			n.log.Info("All active connections closed")

			// Wait to make sure that disconnect queue is not empty
			time.Sleep(time.Second)
		}
	}

	if n.disconnector != nil {
		err := n.disconnector.Shutdown()

		if err != nil {
			n.log.Warnf("%v", err)
		}
	}

	if n.controller != nil {
		err := n.controller.Shutdown()

		if err != nil {
			n.log.Warnf("%v", err)
		}
	}
}

// Authenticate calls controller to perform authentication.
// If authentication is successful, session is registered with a hub.
func (n *Node) Authenticate(s *Session) (err error) {
	res, err := n.controller.Authenticate(s.UID, s.env)

	if err == nil {
		s.Identifiers = res.Identifier
		s.connected = true

		n.hub.AddSession(s)
	} else {
		n.Metrics.Counter(metricsFailedAuths).Inc()
	}

	if res != nil {
		n.handleCallReply(s, res.ToCallResult())
	}

	return
}

// Subscribe subscribes session to a channel
func (n *Node) Subscribe(s *Session, msg *common.Message) (err error) {
	s.smu.Lock()

	if _, ok := s.subscriptions[msg.Identifier]; ok {
		s.Log.Warnf("Already subscribed to %s", msg.Identifier)
		s.smu.Unlock()
		return
	}

	res, err := n.controller.Subscribe(s.UID, s.env, s.Identifiers, msg.Identifier)

	if err != nil {
		s.Log.Errorf("Subscribe error: %v", err)
	} else {
		s.subscriptions[msg.Identifier] = true
		s.Log.Debugf("Subscribed to channel: %s", msg.Identifier)
	}

	s.smu.Unlock()

	if res != nil {
		n.handleCommandReply(s, msg, res)
	}

	return
}

// Unsubscribe unsubscribes session from a channel
func (n *Node) Unsubscribe(s *Session, msg *common.Message) (err error) {
	s.smu.Lock()

	if _, ok := s.subscriptions[msg.Identifier]; !ok {
		s.Log.Warnf("Unknown subscription %s", msg.Identifier)
		s.smu.Unlock()
		return
	}

	res, err := n.controller.Unsubscribe(s.UID, s.env, s.Identifiers, msg.Identifier)

	if err != nil {
		s.Log.Errorf("Unsubscribe error: %v", err)
	} else {
		// Make sure to remove all streams subscriptions
		res.StopAllStreams = true

		delete(s.subscriptions, msg.Identifier)

		s.Log.Debugf("Unsubscribed from channel: %s", msg.Identifier)
	}

	s.smu.Unlock()

	if res != nil {
		n.handleCommandReply(s, msg, res)
	}

	return
}

// Perform executes client command
func (n *Node) Perform(s *Session, msg *common.Message) (err error) {
	s.smu.Lock()

	if _, ok := s.subscriptions[msg.Identifier]; !ok {
		s.Log.Warnf("Unknown subscription %s", msg.Identifier)
		s.smu.Unlock()
		return
	}

	s.smu.Unlock()

	res, err := n.controller.Perform(s.UID, s.env, s.Identifiers, msg.Identifier, msg.Data)

	if err != nil {
		s.Log.Errorf("Perform error: %v", err)
	} else {
		s.Log.Debugf("Perform result: %v", res)
	}

	if res != nil {
		n.handleCommandReply(s, msg, res)
	}

	return
}

// Broadcast message to stream
func (n *Node) Broadcast(msg *common.StreamMessage) {
	n.Metrics.Counter(metricsBroadcastMsg).Inc()
	n.log.Debugf("Incoming pubsub message: %v", msg)
	n.hub.BroadcastMessage(msg)
}

// Disconnect adds session to disconnector queue and unregister session from hub
func (n *Node) Disconnect(s *Session) error {
	n.hub.RemoveSession(s)
	return n.disconnector.Enqueue(s)
}

// DisconnectNow execute disconnect on controller
func (n *Node) DisconnectNow(s *Session) error {
	sessionSubscriptions := subscriptionsList(s.subscriptions)

	s.Log.Debugf("Disconnect %s %s %v %v", s.Identifiers, s.env.URL, s.env.Headers, sessionSubscriptions)

	err := n.controller.Disconnect(
		s.UID,
		s.env,
		s.Identifiers,
		sessionSubscriptions,
	)

	if err != nil {
		s.Log.Errorf("Disconnect error: %v", err)
	}

	return err
}

// RemoteDisconnect find a session by identifier and closes it
func (n *Node) RemoteDisconnect(msg *common.RemoteDisconnectMessage) {
	n.Metrics.Counter(metricsBroadcastMsg).Inc()
	n.log.Debugf("Incoming pubsub command: %v", msg)
	n.hub.RemoteDisconnect(msg)
}

func transmit(s *Session, transmissions []string) {
	for _, msg := range transmissions {
		s.Send([]byte(msg))
	}
}

func (n *Node) handleCommandReply(s *Session, msg *common.Message, reply *common.CommandResult) {
	if reply.Disconnect {
		defer s.Disconnect("Command Failed", CloseAbnormalClosure)
	}

	if reply.StopAllStreams {
		n.hub.RemoveAllSubscriptions(s.UID, msg.Identifier)
	} else if reply.StoppedStreams != nil {
		for _, stream := range reply.StoppedStreams {
			n.hub.RemoveSubscription(s.UID, msg.Identifier, stream)
		}
	}

	if reply.Streams != nil {
		for _, stream := range reply.Streams {
			n.hub.AddSubscription(s.UID, msg.Identifier, stream)
		}
	}

	if reply.IState != nil {
		s.smu.Lock()
		s.env.MergeChannelState(msg.Identifier, &reply.IState)
		s.smu.Unlock()
	}

	n.handleCallReply(s, reply.ToCallResult())
}

func (n *Node) handleCallReply(s *Session, reply *common.CallResult) {
	if reply.CState != nil {
		s.smu.Lock()
		s.env.MergeConnectionState(&reply.CState)
		s.smu.Unlock()
	}

	if reply.Broadcasts != nil {
		for _, broadcast := range reply.Broadcasts {
			n.Broadcast(broadcast)
		}
	}

	if reply.Transmissions != nil {
		transmit(s, reply.Transmissions)
	}
}

func (n *Node) collectStats() {
	statsCollectInterval := time.Duration(n.config.StatsRefreshInterval) * time.Second

	for {
		select {
		case <-n.shutdownCh:
			return
		case <-time.After(statsCollectInterval):
			n.collectStatsOnce()
		}
	}
}

func (n *Node) collectStatsOnce() {
	n.Metrics.Gauge(metricsGoroutines).Set(runtime.NumGoroutine())

	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	n.Metrics.Gauge(metricsMemSys).Set64(int64(m.Sys))

	n.Metrics.Gauge(metricsClientsNum).Set(n.hub.Size())
	n.Metrics.Gauge(metricsUniqClientsNum).Set(n.hub.UniqSize())
	n.Metrics.Gauge(metricsStreamsNum).Set(n.hub.StreamsSize())
	n.Metrics.Gauge(metricsDisconnectQueue).Set(n.disconnector.Size())
}

func (n *Node) registerMetrics() {
	n.Metrics.RegisterGauge(metricsGoroutines, "The number of Go routines")
	n.Metrics.RegisterGauge(metricsMemSys, "The total bytes of memory obtained from the OS")

	n.Metrics.RegisterGauge(metricsClientsNum, "The number of active clients")
	n.Metrics.RegisterGauge(metricsUniqClientsNum, "The number of unique clients (with respect to connection identifiers)")
	n.Metrics.RegisterGauge(metricsStreamsNum, "The number of active broadcasting streams")
	n.Metrics.RegisterGauge(metricsDisconnectQueue, "The size of delayed disconnect")

	n.Metrics.RegisterCounter(metricsFailedAuths, "The total number of failed authentication attempts")
	n.Metrics.RegisterCounter(metricsReceivedMsg, "The total number of received messages from clients")
	n.Metrics.RegisterCounter(metricsUnknownReceived, "The total number of unrecognized messages received from clients")
	n.Metrics.RegisterCounter(metricsBroadcastMsg, "The total number of messages received through PubSub (for broadcast)")
	n.Metrics.RegisterCounter(metricsUnknownBroadcast, "The total number of unrecognized messages received through PubSub")
}

func subscriptionsList(m map[string]bool) []string {
	keys := make([]string, len(m))
	i := 0

	for k := range m {
		keys[i] = k
		i++
	}
	return keys
}

func newDisconnectMessage(reason string, reconnect bool) []byte {
	return (&common.DisconnectMessage{Type: "disconnect", Reason: reason, Reconnect: reconnect}).ToJSON()
}
