package agent

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"

	"golang.org/x/crypto/ssh"
)

// directTCPIP is the RFC 4254 "direct-tcpip" channel-open payload: the client
// asks the server to connect to (Host, Port) and pipe the channel to it.
type directTCPIP struct {
	Host       string
	Port       uint32
	OriginHost string
	OriginPort uint32
}

// handleDirectTCPIP opens a TCP connection from inside the container to the
// requested address and bridges it to the channel. This is how VSCodium reaches
// its own server port and how `ssh -L` local forwards work - required, not
// optional.
func (s *Server) handleDirectTCPIP(ctx context.Context, newChan ssh.NewChannel) {
	req, err := parseDirectTCPIP(newChan.ExtraData())
	if err != nil {
		_ = newChan.Reject(ssh.ConnectionFailed, "bad direct-tcpip payload")
		return
	}

	dialer := net.Dialer{}
	addr := net.JoinHostPort(req.Host, fmt.Sprintf("%d", req.Port))
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		_ = newChan.Reject(ssh.ConnectionFailed, "dial "+addr+": "+err.Error())
		return
	}
	defer conn.Close()

	channel, requests, err := newChan.Accept()
	if err != nil {
		return
	}
	defer channel.Close()
	go ssh.DiscardRequests(requests)

	bridge(channel, conn)
}

// tcpipForward is the RFC 4254 "tcpip-forward" / "cancel-tcpip-forward" global
// request payload: the client asks the server (the agent, inside the container)
// to listen on (BindAddr, BindPort) and forward accepted connections back over
// the ssh connection as "forwarded-tcpip" channels. This is `ssh -R`.
type tcpipForward struct {
	BindAddr string
	BindPort uint32
}

// remoteForwards tracks the listeners a single connection has requested, so they
// can be cancelled individually and all torn down when the connection ends.
type remoteForwards struct {
	conn ssh.Conn
	mu   sync.Mutex
	ln   map[string]net.Listener // key: "addr:port"
}

func newRemoteForwards(conn ssh.Conn) *remoteForwards {
	return &remoteForwards{conn: conn, ln: map[string]net.Listener{}}
}

// handleGlobalRequests services connection-wide requests: tcpip-forward and its
// cancellation (remote port forwarding). Anything else is refused so a client
// that asked for a reply is never left waiting.
func (s *Server) handleGlobalRequests(ctx context.Context, fwds *remoteForwards, reqs <-chan *ssh.Request) {
	for req := range reqs {
		switch req.Type {
		case "tcpip-forward":
			fwds.start(ctx, req)
		case "cancel-tcpip-forward":
			fwds.cancel(req)
		default:
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
		}
	}
}

// start opens a listener for a tcpip-forward request and streams each accepted
// connection back to the client as a forwarded-tcpip channel.
func (f *remoteForwards) start(ctx context.Context, req *ssh.Request) {
	var p tcpipForward
	if err := ssh.Unmarshal(req.Payload, &p); err != nil {
		if req.WantReply {
			_ = req.Reply(false, nil)
		}
		return
	}

	lc := net.ListenConfig{}
	ln, err := lc.Listen(ctx, "tcp", net.JoinHostPort(p.BindAddr, fmt.Sprintf("%d", p.BindPort)))
	if err != nil {
		if req.WantReply {
			_ = req.Reply(false, nil)
		}
		return
	}

	// When the client requested port 0 the kernel picked one; report it back and
	// use it as the bind port in every forwarded-tcpip channel header.
	bindPort := p.BindPort
	if tcp, ok := ln.Addr().(*net.TCPAddr); ok {
		bindPort = uint32(tcp.Port) //nolint:gosec // TCP port fits in uint32
	}
	if req.WantReply {
		var reply []byte
		if p.BindPort == 0 {
			reply = ssh.Marshal(struct{ Port uint32 }{bindPort})
		}
		_ = req.Reply(true, reply)
	}

	f.mu.Lock()
	f.ln[forwardKey(p.BindAddr, p.BindPort)] = ln
	f.mu.Unlock()

	go f.accept(ln, p.BindAddr, bindPort)
}

// accept forwards each incoming connection on ln back to the client.
func (f *remoteForwards) accept(ln net.Listener, bindAddr string, bindPort uint32) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return // listener closed (cancel or connection teardown)
		}
		go f.forward(conn, bindAddr, bindPort)
	}
}

// forwardedTCPIP is the forwarded-tcpip channel-open payload (RFC 4254 sec 7.2).
type forwardedTCPIP struct {
	ConnectedAddr string
	ConnectedPort uint32
	OriginAddr    string
	OriginPort    uint32
}

// forward opens a forwarded-tcpip channel for one accepted connection and bridges
// the two in both directions.
func (f *remoteForwards) forward(conn net.Conn, bindAddr string, bindPort uint32) {
	defer conn.Close()

	originAddr, originPort := splitHostPort(conn.RemoteAddr())
	payload := ssh.Marshal(forwardedTCPIP{
		ConnectedAddr: bindAddr,
		ConnectedPort: bindPort,
		OriginAddr:    originAddr,
		OriginPort:    originPort,
	})
	channel, reqs, err := f.conn.OpenChannel("forwarded-tcpip", payload)
	if err != nil {
		return
	}
	defer channel.Close()
	go ssh.DiscardRequests(reqs)

	bridge(channel, conn)
}

// cancel closes the listener a cancel-tcpip-forward request names.
func (f *remoteForwards) cancel(req *ssh.Request) {
	var p tcpipForward
	if err := ssh.Unmarshal(req.Payload, &p); err != nil {
		if req.WantReply {
			_ = req.Reply(false, nil)
		}
		return
	}
	key := forwardKey(p.BindAddr, p.BindPort)
	f.mu.Lock()
	ln := f.ln[key]
	delete(f.ln, key)
	f.mu.Unlock()
	if ln != nil {
		_ = ln.Close()
	}
	if req.WantReply {
		_ = req.Reply(true, nil)
	}
}

// closeAll shuts every listener down when the connection ends.
func (f *remoteForwards) closeAll() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for key, ln := range f.ln {
		_ = ln.Close()
		delete(f.ln, key)
	}
}

func forwardKey(addr string, port uint32) string {
	return net.JoinHostPort(addr, fmt.Sprintf("%d", port))
}

func splitHostPort(addr net.Addr) (string, uint32) {
	if tcp, ok := addr.(*net.TCPAddr); ok {
		return tcp.IP.String(), uint32(tcp.Port) //nolint:gosec // TCP port fits in uint32
	}
	return "", 0
}

// bridge copies between an ssh channel and a net.Conn in both directions,
// returning when either side closes.
func bridge(channel ssh.Channel, conn net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(conn, channel)
		if c, ok := conn.(interface{ CloseWrite() error }); ok {
			_ = c.CloseWrite()
		}
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(channel, conn)
		_ = channel.CloseWrite()
	}()
	wg.Wait()
}

// parseDirectTCPIP decodes the channel extra-data (four fields: string, uint32,
// string, uint32).
func parseDirectTCPIP(data []byte) (*directTCPIP, error) {
	host, rest, ok := readStringRest(data)
	if !ok {
		return nil, fmt.Errorf("missing host")
	}
	if len(rest) < 4 {
		return nil, fmt.Errorf("missing port")
	}
	port := binary.BigEndian.Uint32(rest[0:4])
	rest = rest[4:]
	origin, rest, ok := readStringRest(rest)
	if !ok {
		return nil, fmt.Errorf("missing origin host")
	}
	if len(rest) < 4 {
		return nil, fmt.Errorf("missing origin port")
	}
	originPort := binary.BigEndian.Uint32(rest[0:4])
	return &directTCPIP{Host: host, Port: port, OriginHost: origin, OriginPort: originPort}, nil
}
