package main

import (
	"bufio"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"sync"
	"time"

	"github.com/savsgio/gotils/strconv"
	"github.com/tidwall/evio"
	"github.com/valyala/fasthttp"
	"golang.org/x/net/websocket"
)

var _ websocket.Conn

func main() {
	var port int
	var loops int
	var udp bool
	var trace bool
	var reuseport bool
	var stdlib bool
	connections := make(map[uint64]evio.Conn)
	lock := sync.RWMutex{}
	seq := uint64(0)

	flag.IntVar(&port, "port", 8972, "server port")
	flag.BoolVar(&udp, "udp", false, "listen on udp")
	flag.BoolVar(&reuseport, "reuseport", false, "reuseport (SO_REUSEPORT)")
	flag.BoolVar(&trace, "trace", false, "print packets to console")
	flag.IntVar(&loops, "loops", 0, "num loops")
	flag.BoolVar(&stdlib, "stdlib", false, "use stdlib")
	flag.Parse()

	var events evio.Events
	events.NumLoops = loops
	events.Serving = func(srv evio.Server) (action evio.Action) {
		log.Printf("echo server started on port %d (loops: %d)", port, srv.NumLoops)
		if reuseport {
			log.Printf("reuseport")
		}
		if stdlib {
			log.Printf("stdlib")
		}
		return
	}
	events.Opened = func(c evio.Conn) (out []byte, opts evio.Options, action evio.Action) {
		lock.Lock()
		defer lock.Unlock()
		seq++
		connections[seq] = c
		c.SetContext(&connContext{id: seq, status: ESTAB})
		if len(connections)%100 == 0 {
			log.Printf("total number of connections: %v", len(connections))
		}
		return
	}
	events.Closed = func(c evio.Conn, err error) (action evio.Action) {
		lock.Lock()
		defer lock.Unlock()
		delete(connections, c.Context().(*connContext).id)
		if len(connections)%100 == 0 {
			log.Printf("total number of connections: %v", len(connections))
		}
		return
	}

	events.Data = func(c evio.Conn, in []byte) (out []byte, action evio.Action) {
		if trace {
			// log.Printf("<-%s", strings.TrimSpace(string(in)))
		}
		ctx := c.Context().(*connContext)
		if ctx.status == ESTAB && len(in) > 0 {
			if ctx.req == nil {
				ctx.req = fasthttp.AcquireRequest()
				err := ctx.req.Read(bufio.NewReader(bytes.NewReader(in)))
				if err != nil {
					log.Printf("ERROR: %s", err)
					if err != io.ErrUnexpectedEOF {
						fasthttp.ReleaseRequest(ctx.req)
						ctx.req = nil
						return nil, evio.Close
					}
				} else {
					if out, err = Upgrade(ctx.req); err != nil {
						log.Printf("ERROR: %s", err)
						fasthttp.ReleaseRequest(ctx.req)
						ctx.req = nil
						return nil, evio.Close
					}
					log.Printf("-> %s", out)
					ctx.status = READY
					return
				}
			}
		}
		if len(in) == 0 {
			// out = []byte("tick\n")
		} else {
			// out = in
			for {
				if err := (&ctx.frameReader).Decode(in); err != nil {
					if err == io.ErrUnexpectedEOF {
						return
					}
					log.Println(err)
					ctx.req = nil
					action = evio.Close
					return
				}
				in = nil
				log.Printf("<- %s", ctx.frameReader.payload.Bytes())
			}
		}
		return
	}

	events.Tick = func() (delay time.Duration, action evio.Action) {
		log.Printf("tick")
		delay = time.Second
		lock.RLock()
		defer lock.RUnlock()
		for _, c := range connections {
			c.Wake()
		}
		return
	}
	scheme := "tcp"
	if udp {
		scheme = "udp"
	}
	if stdlib {
		scheme += "-net"
	}
	log.Fatal(evio.Serve(events, fmt.Sprintf("%s://:%d?reuseport=%t", scheme, port, reuseport)))
}

type Status int

const (
	ESTAB Status = iota
	READY
	BACKOFF
)

type connContext struct {
	id          uint64
	status      Status
	req         *fasthttp.Request
	frameReader frameReader
	toWrite     []byte
}

var badHandshake = errors.New("websocket: the client is not using the websocket protocol")

func Upgrade(r *fasthttp.Request) ([]byte, error) {
	if !r.Header.IsGet() {
		return nil, badHandshake
	}

	if !tokenContainsValue(strconv.B2S(r.Header.Peek("Connection")), "Upgrade") {
		return nil, badHandshake
	}

	if !tokenContainsValue(strconv.B2S(r.Header.Peek("Upgrade")), "Websocket") {
		return nil, badHandshake
	}

	if !tokenContainsValue(strconv.B2S(r.Header.Peek("Sec-Websocket-Version")), "13") {
		return nil, badHandshake
	}

	challengeKey := r.Header.Peek("Sec-Websocket-Key")
	if len(challengeKey) == 0 {
		return nil, badHandshake
	}

	p := make([]byte, 0, 4096)
	p = append(p, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: "...)
	p = append(p, computeAcceptKeyBytes(challengeKey)...)
	p = append(p, "\r\n"...)
	// p = append(p, "Sec-WebSocket-Protocol: chat\r\n"...)
	p = append(p, "\r\n"...)
	return p, nil
}
