package gremtune

import (
	"net/http"
	"time"

	"sync"

	"github.com/gorilla/websocket"
	"github.com/pkg/errors"
)

type dialer interface {
	connect() error
	IsConnected() bool
	IsDisposed() bool
	write([]byte) error
	read() (int, []byte, error)
	close() error
	getAuth() *auth
	ping(errs chan error)
}

/////
/*
WebSocket Connection
*/
/////

// Ws is the dialer for a WebSocket connection
type Ws struct {
	host         string
	conn         *websocket.Conn
	auth         *auth
	disposed     bool
	connected    bool
	pingInterval time.Duration
	writingWait  time.Duration
	readingWait  time.Duration
	timeout      time.Duration
	quit         chan struct{}
	sync.RWMutex
}

//Auth is the container for authentication data of dialer
type auth struct {
	username string
	password string
}

func (ws *Ws) connect() (err error) {
	d := websocket.Dialer{
		WriteBufferSize:  8192,
		ReadBufferSize:   8192,
		HandshakeTimeout: 5 * time.Second, // Timeout or else we'll hang forever and never fail on bad hosts.
	}
	ws.conn, _, err = d.Dial(ws.host, http.Header{})
	if err != nil {

		// As of 3.2.2 the URL has changed.
		// https://groups.google.com/forum/#!msg/gremlin-users/x4hiHsmTsHM/Xe4GcPtRCAAJ
		ws.host = ws.host + "/gremlin"
		ws.conn, _, err = d.Dial(ws.host, http.Header{})
	}

	if err == nil {
		ws.connected = true
		ws.conn.SetPongHandler(func(appData string) error {
			ws.connected = true
			return nil
		})
	}
	return
}

// IsConnected returns whether the underlying websocket is connected
func (ws *Ws) IsConnected() bool {
	return ws.connected
}

// IsDisposed returns whether the underlying websocket is disposed
func (ws *Ws) IsDisposed() bool {
	return ws.disposed
}

func (ws *Ws) write(msg []byte) (err error) {
	err = ws.conn.WriteMessage(2, msg)
	return
}

func (ws *Ws) read() (msgType int, msg []byte, err error) {
	msgType, msg, err = ws.conn.ReadMessage()
	return
}

func (ws *Ws) close() (err error) {
	defer func() {
		close(ws.quit)
		ws.conn.Close()
		ws.disposed = true
	}()

	err = ws.conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "")) //Cleanly close the connection with the server
	return
}

func (ws *Ws) getAuth() *auth {
	if ws.auth == nil {
		panic("You must create a Secure Dialer for authenticate with the server")
	}
	return ws.auth
}

func (ws *Ws) ping(errs chan error) {
	ticker := time.NewTicker(ws.pingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			connected := true
			if err := ws.conn.WriteControl(websocket.PingMessage, []byte{}, time.Now().Add(ws.writingWait)); err != nil {
				errs <- err
				connected = false
			}
			ws.Lock()
			ws.connected = connected
			ws.Unlock()

		case <-ws.quit:
			return
		}
	}
}

func (c *Client) writeWorker(errs chan error, quit chan struct{}) { // writeWorker works on a loop and dispatches messages as soon as it receives them
	for {
		select {
		case msg := <-c.requests:
			c.Lock()
			err := c.conn.write(msg)
			if err != nil {
				errs <- err
				c.Errored = true
				c.Unlock()
				break
			}
			c.Unlock()

		case <-quit:
			return
		}
	}
}

func (c *Client) readWorker(errs chan error, quit chan struct{}) { // readWorker works on a loop and sorts messages as soon as it receives them
	for {
		msgType, msg, err := c.conn.read()
		if msgType == -1 { // msgType == -1 is noFrame (close connection)
			return
		}
		if err != nil {
			errs <- errors.Wrapf(err, "Receive message type: %d", msgType)
			c.Errored = true
			break
		}
		if msg != nil {
			c.handleResponse(msg)
		}

		select {
		case <-quit:
			return
		default:
			continue
		}
	}
}
