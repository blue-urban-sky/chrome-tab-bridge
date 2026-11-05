package main

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

const hostName = "local.chrome_tab_bridge"

type hostRequest struct {
	Cmd     string          `json:"cmd"`
	Args    json.RawMessage `json:"args,omitempty"`
	Timeout int             `json:"timeout,omitempty"`
}

type reply struct {
	ID     string          `json:"id,omitempty"`
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

type host struct {
	out     io.Writer
	outMu   sync.Mutex
	mu      sync.Mutex
	pending map[string]chan reply
	seq     atomic.Uint64
}

func runDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "Application Support", "chrome-tab-bridge", "run")
}

func runHost() error {
	return serveHost(runDir(), os.Stdin, os.Stdout)
}

func serveHost(dir string, in io.Reader, out io.Writer) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	ln, err := net.Listen("unix", filepath.Join(dir, strconv.Itoa(os.Getpid())+".sock"))
	if err != nil {
		return err
	}
	defer ln.Close()
	h := &host{out: out, pending: map[string]chan reply{}}
	go h.accept(ln)
	return h.readChrome(in)
}

func (h *host) accept(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go h.serve(c)
	}
}

func (h *host) serve(c net.Conn) {
	defer c.Close()
	var req hostRequest
	if err := json.NewDecoder(c).Decode(&req); err != nil {
		return
	}
	_ = json.NewEncoder(c).Encode(h.forward(req))
}

func (h *host) forward(req hostRequest) reply {
	id := strconv.FormatUint(h.seq.Add(1), 10)
	ch := make(chan reply, 1)
	h.mu.Lock()
	h.pending[id] = ch
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.pending, id)
		h.mu.Unlock()
	}()

	args := req.Args
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	if err := h.send(map[string]any{"id": id, "cmd": req.Cmd, "args": args}); err != nil {
		return reply{Error: "writing to Chrome: " + err.Error()}
	}
	timeout := time.Duration(req.Timeout) * time.Second
	if timeout <= 0 {
		timeout = time.Minute
	}
	select {
	case r := <-ch:
		r.ID = ""
		return r
	case <-time.After(timeout):
		return reply{Error: "timed out waiting for Chrome"}
	}
}

func (h *host) send(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	frame := binary.NativeEndian.AppendUint32(nil, uint32(len(data)))
	h.outMu.Lock()
	defer h.outMu.Unlock()
	_, err = h.out.Write(append(frame, data...))
	return err
}

func (h *host) readChrome(in io.Reader) error {
	r := bufio.NewReader(in)
	header := make([]byte, 4)
	for {
		if _, err := io.ReadFull(r, header); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return nil
			}
			return err
		}
		body := make([]byte, binary.NativeEndian.Uint32(header))
		if _, err := io.ReadFull(r, body); err != nil {
			return nil
		}
		var msg reply
		if json.Unmarshal(body, &msg) != nil {
			continue
		}
		h.mu.Lock()
		ch := h.pending[msg.ID]
		h.mu.Unlock()
		if ch != nil {
			select {
			case ch <- msg:
			default:
			}
		}
	}
}
