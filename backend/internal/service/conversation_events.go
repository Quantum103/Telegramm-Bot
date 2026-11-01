package service

import "sync"

// conversationEventHub рассылает сигнал об изменении диалогов всем открытым панелям.
// Само сообщение по WebSocket не передаётся: клиент перечитывает его через защищённый API.
type conversationEventHub struct {
	mu          sync.Mutex
	nextID      uint64
	subscribers map[uint64]chan struct{}
}

func newConversationEventHub() *conversationEventHub {
	return &conversationEventHub{subscribers: make(map[uint64]chan struct{})}
}

func (h *conversationEventHub) subscribe() (<-chan struct{}, func()) {
	h.mu.Lock()
	h.nextID++
	id := h.nextID
	events := make(chan struct{}, 1)
	h.subscribers[id] = events
	h.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			h.mu.Lock()
			if current, ok := h.subscribers[id]; ok {
				delete(h.subscribers, id)
				close(current)
			}
			h.mu.Unlock()
		})
	}
	return events, cancel
}

// publish использует буфер на один элемент: несколько быстрых сообщений сворачиваются
// в один сигнал перечитать историю, поэтому медленный клиент не блокирует отправителя.
func (h *conversationEventHub) publish() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, events := range h.subscribers {
		select {
		case events <- struct{}{}:
		default:
		}
	}
}
