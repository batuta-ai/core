package acp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	maxFrameBytes         = 1 << 20
	maxPending            = 32
	maxNotifications      = 16
	defaultRequestTimeout = 30 * time.Second
)

var (
	ErrClosed             = errors.New("acp: connection closed")
	ErrProtocol           = errors.New("acp: invalid JSON-RPC message")
	ErrFrameTooLarge      = errors.New("acp: frame exceeds limit")
	ErrCapacity           = errors.New("acp: queue capacity exceeded")
	ErrVersion            = errors.New("acp: unsupported protocol version")
	ErrAlreadyInitialized = errors.New("acp: initialize already attempted")
	ErrTransport          = errors.New("acp: transport I/O failed")
	ErrOptions            = errors.New("acp: invalid connection options")
)

// Options can reduce the fixed memory ceilings. Zero selects the default.
// RequestTimeout bounds calls, writes and unanswered inbound permission requests,
// even when their caller supplies a context without a deadline.
type Options struct {
	MaxFrameBytes      int
	MaxPending         int
	NotificationBuffer int
	RequestTimeout     time.Duration
}

type reply struct {
	result json.RawMessage
	err    error
}
type writeJob struct {
	payload []byte
	result  chan error
}

// Connection owns its reader and writer. Their Close methods must promptly
// unblock concurrent I/O (as os pipes and net connections do). It does not own
// or reap the agent process; Done proves only transport goroutine shutdown.
// There is no retry: a timeout or transport failure makes this attempt terminal.
type Connection struct {
	reader        io.ReadCloser
	writer        io.WriteCloser
	options       Options
	writes        chan writeJob
	slots         chan struct{}
	notifications chan Notification
	requests      chan Request
	stopped       chan struct{}
	done          chan struct{}
	stopOnce      sync.Once
	mu            sync.Mutex
	err           error
	nextID        uint64
	initialized   bool
	pending       map[string]chan reply
	incoming      map[string]*time.Timer
}

func NewConnection(reader io.ReadCloser, writer io.WriteCloser, options Options) (*Connection, error) {
	if options.MaxFrameBytes == 0 {
		options.MaxFrameBytes = maxFrameBytes
	}
	if options.MaxPending == 0 {
		options.MaxPending = maxPending
	}
	if options.NotificationBuffer == 0 {
		options.NotificationBuffer = maxNotifications
	}
	if options.RequestTimeout == 0 {
		options.RequestTimeout = defaultRequestTimeout
	}
	if reader == nil || writer == nil || options.MaxFrameBytes < 1 || options.MaxFrameBytes > maxFrameBytes || options.MaxPending < 1 || options.MaxPending > maxPending || options.NotificationBuffer < 1 || options.NotificationBuffer > maxNotifications || options.RequestTimeout < 0 {
		return nil, ErrOptions
	}
	c := &Connection{
		reader:        reader,
		writer:        writer,
		options:       options,
		writes:        make(chan writeJob),
		slots:         make(chan struct{}, options.MaxPending),
		notifications: make(chan Notification, options.NotificationBuffer),
		requests:      make(chan Request, options.MaxPending),
		stopped:       make(chan struct{}),
		done:          make(chan struct{}),
		pending:       make(map[string]chan reply),
		incoming:      make(map[string]*time.Timer),
	}
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		c.readLoop()
	}()
	go func() {
		defer workers.Done()
		c.writeLoop()
	}()
	go func() {
		workers.Wait()
		close(c.notifications)
		close(c.requests)
		close(c.done)
	}()
	return c, nil
}

func (c *Connection) Done() <-chan struct{}              { return c.done }
func (c *Connection) Notifications() <-chan Notification { return c.notifications }
func (c *Connection) Requests() <-chan Request           { return c.requests }
func (c *Connection) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}
func (c *Connection) Close() error {
	c.fail(ErrClosed)
	<-c.done
	return nil
}

func (c *Connection) fail(err error) {
	c.stopOnce.Do(func() {
		c.mu.Lock()
		c.err = err
		for key, timer := range c.incoming {
			timer.Stop()
			delete(c.incoming, key)
		}
		clear(c.pending)
		close(c.stopped)
		c.mu.Unlock()
		_ = c.reader.Close()
		_ = c.writer.Close()
	})
}

// Initialize may be attempted only once. No optional client capabilities are
// advertised: filesystem, terminal and authentication UI methods are absent.
func (c *Connection) Initialize(ctx context.Context, info Implementation) (InitializeResult, error) {
	c.mu.Lock()
	if c.initialized {
		c.mu.Unlock()
		return InitializeResult{}, ErrAlreadyInitialized
	}
	c.initialized = true
	c.mu.Unlock()
	if len(info.Name)+len(info.Version)+len(info.Title) > c.options.MaxFrameBytes {
		return InitializeResult{}, ErrFrameTooLarge
	}
	params, err := json.Marshal(struct {
		ProtocolVersion    int            `json:"protocolVersion"`
		ClientCapabilities struct{}       `json:"clientCapabilities"`
		ClientInfo         Implementation `json:"clientInfo"`
	}{ProtocolVersion: ProtocolVersion, ClientInfo: info})
	if err != nil {
		return InitializeResult{}, ErrProtocol
	}
	raw, err := c.Call(ctx, "initialize", params)
	if err != nil {
		c.fail(err)
		return InitializeResult{}, err
	}
	var result InitializeResult
	if json.Unmarshal(raw, &result) != nil {
		c.fail(ErrProtocol)
		return InitializeResult{}, ErrProtocol
	}
	if result.ProtocolVersion != ProtocolVersion {
		c.fail(ErrVersion)
		return InitializeResult{}, ErrVersion
	}
	return result, nil
}

// Call sends one request and correlates its response. params is bounded before
// encoding. A failure after sending may mean the agent already performed work.
func (c *Connection) Call(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	if err := c.acquire(ctx); err != nil {
		return nil, err
	}
	defer c.release()
	c.mu.Lock()
	if c.err != nil {
		err := c.err
		c.mu.Unlock()
		return nil, err
	}
	c.nextID++
	id := json.RawMessage(strconv.Quote("batuta-" + strconv.FormatUint(c.nextID, 10)))
	key, _ := idKey(id)
	response := make(chan reply, 1)
	c.pending[key] = response
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.pending, key); c.mu.Unlock() }()
	payload, err := c.encode(message{JSONRPC: "2.0", ID: id, Method: method, Params: params})
	if err != nil {
		return nil, err
	}
	callCtx, cancel := context.WithTimeout(ctx, c.options.RequestTimeout)
	defer cancel()
	if err := c.send(callCtx, payload); err != nil {
		// A peer can close stdout immediately after its complete response,
		// before the writer goroutine has delivered its local acknowledgement.
		if errors.Is(err, io.EOF) {
			select {
			case got := <-response:
				return got.result, got.err
			default:
			}
		}
		return nil, err
	}
	select {
	case got := <-response:
		return got.result, got.err
	case <-callCtx.Done():
		c.fail(callCtx.Err())
		return nil, callCtx.Err()
	case <-c.stopped:
		select {
		case got := <-response:
			return got.result, got.err
		default:
			return nil, c.Err()
		}
	}
}

func (c *Connection) Notify(ctx context.Context, method string, params json.RawMessage) error {
	if err := c.acquire(ctx); err != nil {
		return err
	}
	defer c.release()
	payload, err := c.encode(message{JSONRPC: "2.0", Method: method, Params: params})
	if err != nil {
		return err
	}
	writeCtx, cancel := context.WithTimeout(ctx, c.options.RequestTimeout)
	defer cancel()
	return c.send(writeCtx, payload)
}

// Respond answers an outstanding permission request. No permission is ever
// granted by this transport. RPC error responses contain a fixed safe message.
func (c *Connection) Respond(ctx context.Context, id json.RawMessage, result json.RawMessage, rpcErr *RPCError) error {
	if err := c.acquire(ctx); err != nil {
		return err
	}
	defer c.release()
	if len(id) > c.options.MaxFrameBytes {
		return ErrFrameTooLarge
	}
	key, err := idKey(id)
	if err != nil {
		return err
	}
	response := message{JSONRPC: "2.0", ID: id, Result: result}
	if rpcErr != nil {
		response.Result = nil
		response.Error = &wireError{Code: rpcErr.Code, Message: "Request rejected"}
	} else if len(result) == 0 {
		response.Result = json.RawMessage("null")
	}
	payload, err := c.encode(response)
	if err != nil {
		return err
	}
	c.mu.Lock()
	timer, found := c.incoming[key]
	if found {
		timer.Stop()
		delete(c.incoming, key)
	}
	c.mu.Unlock()
	if !found {
		return ErrProtocol
	}
	writeCtx, cancel := context.WithTimeout(ctx, c.options.RequestTimeout)
	defer cancel()
	return c.send(writeCtx, payload)
}

func (c *Connection) acquire(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.Err(); err != nil {
		return err
	}
	select {
	case c.slots <- struct{}{}:
		return nil
	default:
		return ErrCapacity
	}
}
func (c *Connection) release() { <-c.slots }

func (c *Connection) encode(msg message) ([]byte, error) {
	if len(msg.Method)+len(msg.Params)+len(msg.Result)+len(msg.ID) > c.options.MaxFrameBytes {
		return nil, ErrFrameTooLarge
	}
	if !utf8.ValidString(msg.Method) || !validParams(msg.Params) {
		return nil, ErrProtocol
	}
	if len(msg.ID) == 0 && msg.Method == "" {
		return nil, ErrProtocol
	}
	if len(msg.Result) > 0 && (!utf8.Valid(msg.Result) || !json.Valid(msg.Result)) {
		return nil, ErrProtocol
	}
	// Requests always have a method; responses have exactly one outcome.
	if msg.Method == "" && len(msg.Result) == 0 && msg.Error == nil {
		return nil, ErrProtocol
	}
	payload, err := json.Marshal(msg)
	if err != nil {
		return nil, ErrProtocol
	}
	if len(payload) > c.options.MaxFrameBytes {
		return nil, ErrFrameTooLarge
	}
	return append(payload, '\n'), nil
}

func (c *Connection) send(ctx context.Context, payload []byte) error {
	if err := ctx.Err(); err != nil {
		c.fail(err)
		return err
	}
	job := writeJob{payload: payload, result: make(chan error, 1)}
	select {
	case <-c.stopped:
		return c.Err()
	case <-ctx.Done():
		c.fail(ctx.Err())
		return ctx.Err()
	case c.writes <- job:
	}
	select {
	case err := <-job.result:
		return err
	case <-c.stopped:
		return c.Err()
	case <-ctx.Done():
		c.fail(ctx.Err())
		return ctx.Err()
	}
}

func (c *Connection) writeLoop() {
	for {
		select {
		case <-c.stopped:
			return
		case job := <-c.writes:
			if c.Err() != nil {
				return
			}
			n, err := c.writer.Write(job.payload)
			if err != nil || n != len(job.payload) {
				c.fail(ErrTransport)
				job.result <- ErrTransport
				return
			}
			job.result <- nil
		}
	}
}

func (c *Connection) readLoop() {
	scanner := bufio.NewScanner(c.reader)
	scanner.Buffer(make([]byte, min(c.options.MaxFrameBytes+1, 4096)), c.options.MaxFrameBytes+1)
	scanner.Split(func(data []byte, atEOF bool) (int, []byte, error) {
		if newline := bytes.IndexByte(data, '\n'); newline >= 0 {
			if newline > c.options.MaxFrameBytes {
				return 0, nil, ErrFrameTooLarge
			}
			return newline + 1, bytes.TrimSuffix(data[:newline], []byte{'\r'}), nil
		}
		if len(data) > c.options.MaxFrameBytes {
			return 0, nil, ErrFrameTooLarge
		}
		if atEOF && len(data) > 0 {
			return 0, nil, ErrProtocol
		}
		return 0, nil, nil
	})
	for scanner.Scan() {
		if c.Err() != nil {
			return
		}
		msg, err := decodeMessage(scanner.Bytes())
		if err == nil {
			err = c.receive(msg)
		}
		if err != nil {
			c.fail(err)
			return
		}
	}
	err := scanner.Err()
	switch {
	case err == nil:
		c.fail(io.EOF)
	case errors.Is(err, ErrFrameTooLarge), errors.Is(err, ErrProtocol):
		c.fail(err)
	default:
		c.fail(ErrTransport)
	}
}

func (c *Connection) receive(msg message) error {
	if msg.Method == "" {
		key, _ := idKey(msg.ID)
		c.mu.Lock()
		response, found := c.pending[key]
		if found {
			delete(c.pending, key)
			got := reply{result: msg.Result}
			if msg.Error != nil {
				got.err = &RPCError{Code: msg.Error.Code}
			}
			response <- got
		}
		c.mu.Unlock()
		if !found {
			return ErrProtocol
		}
		return nil
	}
	if len(msg.ID) == 0 {
		if msg.Method != "session/update" {
			return nil
		}
		select {
		case c.notifications <- Notification{Method: msg.Method, Params: msg.Params}:
			return nil
		default:
			return ErrCapacity
		}
	}
	if msg.Method != "session/request_permission" {
		payload, err := c.encode(message{JSONRPC: "2.0", ID: msg.ID, Error: &wireError{Code: -32601, Message: "Method not found"}})
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), c.options.RequestTimeout)
		defer cancel()
		return c.send(ctx, payload)
	}
	key, _ := idKey(msg.ID)
	c.mu.Lock()
	if c.err != nil {
		err := c.err
		c.mu.Unlock()
		return err
	}
	if _, found := c.incoming[key]; found {
		c.mu.Unlock()
		return ErrProtocol
	}
	if len(c.incoming) >= c.options.MaxPending {
		c.mu.Unlock()
		return ErrCapacity
	}
	c.incoming[key] = time.AfterFunc(c.options.RequestTimeout, func() { c.fail(context.DeadlineExceeded) })
	c.mu.Unlock()
	select {
	case c.requests <- Request{ID: msg.ID, Method: msg.Method, Params: msg.Params}:
		return nil
	default:
		return ErrCapacity
	}
}
