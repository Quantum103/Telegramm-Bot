package service

import (
	"testing"
	"time"
)

func TestConversationEventHubBroadcastsAndCoalescesChanges(t *testing.T) {
	hub := newConversationEventHub()
	first, unsubscribeFirst := hub.subscribe()
	defer unsubscribeFirst()
	second, unsubscribeSecond := hub.subscribe()
	defer unsubscribeSecond()

	hub.publish()
	hub.publish()

	for name, events := range map[string]<-chan struct{}{"first": first, "second": second} {
		select {
		case _, ok := <-events:
			if !ok {
				t.Fatalf("%s subscription closed before unsubscribe", name)
			}
		case <-time.After(time.Second):
			t.Fatalf("%s subscriber did not receive an event", name)
		}
		select {
		case <-events:
			t.Fatalf("%s subscriber should coalesce repeated changes into one refresh", name)
		default:
		}
	}
}

func TestConversationEventHubUnsubscribeClosesOnlyItsSubscription(t *testing.T) {
	hub := newConversationEventHub()
	first, unsubscribeFirst := hub.subscribe()
	second, unsubscribeSecond := hub.subscribe()
	defer unsubscribeSecond()

	unsubscribeFirst()
	if _, ok := <-first; ok {
		t.Fatal("unsubscribed channel must be closed")
	}
	hub.publish()
	select {
	case _, ok := <-second:
		if !ok {
			t.Fatal("remaining subscriber was unexpectedly closed")
		}
	case <-time.After(time.Second):
		t.Fatal("remaining subscriber did not receive the event")
	}
}
