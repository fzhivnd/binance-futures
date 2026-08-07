package exchange

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/coder/websocket"
)

type MessageHandler func(msgType string, data []byte)

type WSConnection struct {
	url          string
	handler      MessageHandler
	staleTimeout time.Duration
	pingInterval time.Duration
	baseBackoff  time.Duration
	maxBackoff   time.Duration
	maxFailures  int
	onKillSwitch func()
	// idleWhenUnsubscribed marks connections that legitimately sit with zero
	// subscriptions between funding windows (the top-20 kline/bookTicker
	// streams) — staleness is only enforced for these while subscriptions
	// are actually active.
	idleWhenUnsubscribed bool
	// disableStaleCheck marks connections whose message arrival is inherently
	// sparse and unrelated to connection health (e.g. user-data streams only
	// push on account activity — fills, balance changes). For these, the
	// periodic WS ping is the only liveness signal; a long gap with no
	// business messages is normal, not stale.
	disableStaleCheck bool

	mu            sync.Mutex
	conn          *websocket.Conn
	subscriptions []string
	lastMessage   time.Time
	failures      int
}

func NewWSConnection(
	url string,
	handler MessageHandler,
	staleTimeout, pingInterval, baseBackoff, maxBackoff time.Duration,
	maxFailures int,
	onKillSwitch func(),
) *WSConnection {
	return &WSConnection{
		url:          url,
		handler:      handler,
		staleTimeout: staleTimeout,
		pingInterval: pingInterval,
		baseBackoff:  baseBackoff,
		maxBackoff:   maxBackoff,
		maxFailures:  maxFailures,
		onKillSwitch: onKillSwitch,
	}
}

// SetIdleWhenUnsubscribed marks this connection as one that legitimately has
// zero subscriptions between funding windows (see idleWhenUnsubscribed).
func (w *WSConnection) SetIdleWhenUnsubscribed(idle bool) {
	w.idleWhenUnsubscribed = idle
}

// DisableStaleCheck marks this connection as one whose message flow is
// inherently sparse and not a health signal (see disableStaleCheck).
func (w *WSConnection) DisableStaleCheck(disabled bool) {
	w.disableStaleCheck = disabled
}

func (w *WSConnection) Run(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		if err := w.connect(ctx); err != nil {
			slog.Warn("ws connect failed", "error", err)
			w.failures++
			if w.failures >= w.maxFailures {
				if w.onKillSwitch != nil {
					slog.Error("ws max failures reached, activating kill switch", "url", w.url)
					w.onKillSwitch()
					return
				}
				// Non-critical stream: reset and keep retrying rather than silently dying.
				slog.Error("ws max failures reached, resetting and retrying", "url", w.url, "failures", w.failures)
				w.failures = 0
			}
			backoff := w.calcBackoff()
			slog.Warn("ws reconnecting", "attempt", w.failures, "backoff", backoff)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			continue
		}
		w.failures = 0
	}
}

func (w *WSConnection) connect(ctx context.Context) error {
	conn, _, err := websocket.Dial(ctx, w.url, nil)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	conn.SetReadLimit(4 * 1024 * 1024) // 4MB — all-market mark price array can exceed 32KB default

	w.mu.Lock()
	w.conn = conn
	w.lastMessage = time.Now()
	subs := make([]string, len(w.subscriptions))
	copy(subs, w.subscriptions)
	w.mu.Unlock()

	if len(subs) > 0 {
		if err := w.sendSubscribe(ctx, subs); err != nil {
			w.mu.Lock()
			w.conn = nil
			w.mu.Unlock()
			conn.Close(websocket.StatusNormalClosure, "")
			return fmt.Errorf("resubscribe: %w", err)
		}
	}

	slog.Info("ws connected", "url", w.url)

	return w.readLoop(ctx, conn)
}

func (w *WSConnection) readLoop(ctx context.Context, conn *websocket.Conn) error {
	staleTicker := time.NewTicker(w.staleTimeout / 2)
	defer staleTicker.Stop()

	pingTicker := time.NewTicker(w.pingInterval)
	defer pingTicker.Stop()

	readCh := make(chan []byte, 64)
	errCh := make(chan error, 1)

	go func() {
		for {
			_, msg, err := conn.Read(ctx)
			if err != nil {
				errCh <- err
				return
			}
			readCh <- msg
		}
	}()

	for {
		select {
		case <-ctx.Done():
			conn.Close(websocket.StatusNormalClosure, "shutdown")
			return nil

		case err := <-errCh:
			return fmt.Errorf("read: %w", err)

		case msg := <-readCh:
			w.mu.Lock()
			w.lastMessage = time.Now()
			w.mu.Unlock()
			w.dispatch(msg)

		case <-pingTicker.C:
			if err := conn.Ping(ctx); err != nil {
				return fmt.Errorf("ping: %w", err)
			}

		case <-staleTicker.C:
			if w.disableStaleCheck {
				continue
			}
			w.mu.Lock()
			last := w.lastMessage
			hasSubs := len(w.subscriptions) > 0
			idleOk := w.idleWhenUnsubscribed && !hasSubs
			w.mu.Unlock()
			if idleOk {
				continue
			}
			if time.Since(last) > w.staleTimeout {
				conn.Close(websocket.StatusNormalClosure, "stale")
				return fmt.Errorf("stale connection: no message for %s", w.staleTimeout)
			}
		}
	}
}

func (w *WSConnection) dispatch(data []byte) {
	// Detect message type by peeking at "e" field
	var peek struct {
		EventType string `json:"e"`
		Stream    string `json:"stream"`
	}
	_ = json.Unmarshal(data, &peek)

	msgType := peek.EventType
	if msgType == "" {
		msgType = peek.Stream
	}

	w.handler(msgType, data)
}

func (w *WSConnection) Subscribe(ctx context.Context, streams []string) error {
	w.mu.Lock()
	existing := make(map[string]bool)
	for _, s := range w.subscriptions {
		existing[s] = true
	}
	var newStreams []string
	for _, s := range streams {
		if !existing[s] {
			newStreams = append(newStreams, s)
			w.subscriptions = append(w.subscriptions, s)
		}
	}
	conn := w.conn
	w.mu.Unlock()

	if len(newStreams) == 0 || conn == nil {
		return nil
	}
	return w.sendSubscribe(ctx, newStreams)
}

func (w *WSConnection) Unsubscribe(ctx context.Context, streams []string) error {
	w.mu.Lock()
	toRemove := make(map[string]bool)
	for _, s := range streams {
		toRemove[s] = true
	}
	var kept []string
	for _, s := range w.subscriptions {
		if !toRemove[s] {
			kept = append(kept, s)
		}
	}
	w.subscriptions = kept
	conn := w.conn
	w.mu.Unlock()

	if conn == nil {
		return nil
	}
	return w.sendUnsubscribe(ctx, streams)
}

func (w *WSConnection) sendSubscribe(ctx context.Context, streams []string) error {
	return w.sendRequest(ctx, WsSubscribeRequest{Method: "SUBSCRIBE", Params: streams, ID: 1})
}

func (w *WSConnection) sendUnsubscribe(ctx context.Context, streams []string) error {
	return w.sendRequest(ctx, WsSubscribeRequest{Method: "UNSUBSCRIBE", Params: streams, ID: 2})
}

func (w *WSConnection) sendRequest(ctx context.Context, req WsSubscribeRequest) error {
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	w.mu.Lock()
	conn := w.conn
	w.mu.Unlock()
	if conn == nil {
		return fmt.Errorf("not connected")
	}
	return conn.Write(ctx, websocket.MessageText, data)
}

func (w *WSConnection) calcBackoff() time.Duration {
	backoff := w.baseBackoff
	for i := 1; i < w.failures; i++ {
		backoff *= 2
		if backoff > w.maxBackoff {
			backoff = w.maxBackoff
			break
		}
	}
	return backoff
}

func (w *WSConnection) LastMessageTime() time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.lastMessage
}
