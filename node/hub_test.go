package node

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestUnsubscribeRaceConditions(t *testing.T) {
	hub := NewHub(2)
	node := NewMockNode()

	go hub.Run()
	defer hub.Shutdown()

	session := NewMockSession("123", &node)
	session2 := NewMockSession("321", &node)

	hub.addSession(session)
	hub.subscribeSession("123", "test", "test_channel")

	hub.addSession(session2)
	hub.subscribeSession("321", "test", "test_channel")

	hub.Broadcast("test", "hello")

	_, err := session.ws.Read()
	assert.Nil(t, err)

	_, err = session2.ws.Read()
	assert.Nil(t, err)

	assert.Equal(t, 2, hub.Size(), "Connections size must be equal 2")

	go func() {
		hub.Broadcast("test", "pong")
		hub.RemoveSession(session)
		hub.Broadcast("test", "ping")
	}()

	go func() {
		hub.Broadcast("test", "bye-bye")
	}()

	_, err = session2.ws.Read()
	assert.Nil(t, err)
	_, err = session2.ws.Read()
	assert.Nil(t, err)
	_, err = session2.ws.Read()
	assert.Nil(t, err)
	_, err = session.ws.Read()
	assert.Nil(t, err)

	assert.Equal(t, 1, hub.Size(), "Connections size must be equal 1")
}

func TestUnsubscribeSession(t *testing.T) {
	hub := NewHub(2)
	node := NewMockNode()

	go hub.Run()
	defer hub.Shutdown()

	session := NewMockSession("123", &node)
	hub.addSession(session)

	hub.subscribeSession("123", "test", "test_channel")
	hub.subscribeSession("123", "test2", "test_channel")

	hub.Broadcast("test", "\"hello\"")

	msg, err := session.ws.Read()
	assert.Nil(t, err)
	assert.Equal(t, "{\"identifier\":\"test_channel\",\"message\":\"hello\"}", string(msg))

	hub.unsubscribeSession("123", "test", "test_channel")

	hub.Broadcast("test", "\"goodbye\"")

	_, err = session.ws.Read()
	assert.NotNil(t, err)

	hub.Broadcast("test2", "\"bye\"")

	msg, err = session.ws.Read()
	assert.Nil(t, err)
	assert.Equal(t, "{\"identifier\":\"test_channel\",\"message\":\"bye\"}", string(msg))

	hub.unsubscribeSessionFromAllChannels("123")

	hub.Broadcast("test2", "\"goodbye\"")

	msg, err = session.ws.Read()
	assert.NotNil(t, err)
}

func TestSubscribeSession(t *testing.T) {
	hub := NewHub(2)
	node := NewMockNode()

	go hub.Run()
	defer hub.Shutdown()

	session := NewMockSession("123", &node)
	hub.addSession(session)

	t.Run("Subscribe to a single channel", func(t *testing.T) {
		hub.subscribeSession("123", "test", "test_channel")

		hub.Broadcast("test", "\"hello\"")

		msg, err := session.ws.Read()
		assert.Nil(t, err)
		assert.Equal(t, "{\"identifier\":\"test_channel\",\"message\":\"hello\"}", string(msg))
	})

	t.Run("Successful to the same stream from multiple channels", func(t *testing.T) {
		hub.subscribeSession("123", "test", "test_channel")
		hub.subscribeSession("123", "test", "test_channel2")

		hub.Broadcast("test", "\"hello twice\"")

		received := []string{}

		msg, err := session.ws.Read()
		assert.Nil(t, err)
		received = append(received, string(msg))

		msg, err = session.ws.Read()
		assert.Nil(t, err)
		received = append(received, string(msg))

		assert.Contains(t, received, "{\"identifier\":\"test_channel\",\"message\":\"hello twice\"}")
		assert.Contains(t, received, "{\"identifier\":\"test_channel2\",\"message\":\"hello twice\"}")
	})
}

func TestBuildMessageJSON(t *testing.T) {
	expected := []byte("{\"identifier\":\"chat\",\"message\":{\"text\":\"hello!\"}}")
	actual := buildMessage("{\"text\":\"hello!\"}", "chat")
	assert.Equal(t, expected, actual)
}

func TestBuildMessageString(t *testing.T) {
	expected := []byte("{\"identifier\":\"chat\",\"message\":\"plain string\"}")
	actual := buildMessage("\"plain string\"", "chat")
	assert.Equal(t, expected, actual)
}
