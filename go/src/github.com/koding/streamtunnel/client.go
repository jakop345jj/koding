package streamtunnel

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/koding/logging"
)

// Client is responsible for creating a control connection to a tunnel server,
// creating new tunnels and proxy them to tunnel server.
type Client struct {
	// underlying tcp connection which is used for multiplexing
	nc net.Conn

	// underlying yamux session
	session *yamux.Session

	// serverAddr is the address of the tunnel-server
	serverAddr string

	// localAddr is the address of a local server that will be tunneled to the
	// public. Currently only one server is supported.
	localAddr string

	// yamuxConfig is passed to new yamux.Session's
	yamuxConfig *yamux.Config

	log logging.Logger
}

// ClientConfig defines the configuration for the Client
type ClientConfig struct {
	// ServerAddr defines the TCP address of the tunnel server to be connected
	ServerAddr string

	// LocalAddr defines the TCP address of the local server. This is optional
	// if you want to specify a single TCP address. Otherwise the client will
	// always proxy to 127.0.0.1:incomingPort, where incomingPort is the
	// tunnelserver's public exposed Port
	LocalAddr string

	// Debug enables debug mode, enable only if you want to debug the server
	Debug bool

	// Log defines the logger. If nil a default logging.Logger is used.
	Log logging.Logger

	// YamuxConfig defines the config which passed to every new yamux.Session. If nil
	// yamux.DefaultConfig() is used.
	YamuxConfig *yamux.Config
}

// NewClient creates a new tunnel that is established between the serverAddr
// and localAddr. It exits if it can't create a new control connection to the
// server. If localAddr is empty client will always try to proxy to a local
// port.
func NewClient(cfg *ClientConfig) *Client {
	yamuxConfig := yamux.DefaultConfig()
	if cfg.YamuxConfig != nil {
		if err := yamux.VerifyConfig(cfg.YamuxConfig); err != nil {
			fmt.Fprintf(os.Stderr, err.Error())
			os.Exit(1)
		}

		yamuxConfig = cfg.YamuxConfig
	}

	log := newLogger("streamtunnel-server", cfg.Debug)
	if cfg.Log != nil {
		log = cfg.Log
	}

	client := &Client{
		serverAddr:  cfg.ServerAddr,
		localAddr:   cfg.LocalAddr,
		log:         log,
		yamuxConfig: yamuxConfig,
	}

	return client
}

func (c *Client) Start(identifier string) error {
	var err error
	c.nc, err = net.Dial("tcp", c.serverAddr)
	if err != nil {
		return err
	}

	remoteAddr := fmt.Sprintf("http://%s%s", c.nc.RemoteAddr(), ControlPath)
	req, err := http.NewRequest("CONNECT", remoteAddr, nil)
	if err != nil {
		return fmt.Errorf("CONNECT %s", err)
	}

	req.Header.Set(XKTunnelIdentifier, identifier)
	if err := req.Write(c.nc); err != nil {
		return err
	}

	resp, err := http.ReadResponse(bufio.NewReader(c.nc), req)
	if err != nil {
		return fmt.Errorf("read response %s", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 && resp.Status != Connected {
		out, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			return err
		}

		return fmt.Errorf("proxy server: %s. err: %s", resp.Status, string(out))
	}

	c.session, err = yamux.Client(c.nc, c.yamuxConfig)
	if err != nil {
		return err
	}

	var stream net.Conn

	// if we don't receive anything from the server, we'll timeout
	select {
	case <-async(func() {
		// this is blocking until client opens a session to us
		stream, err = c.session.Open()
		if err != nil {
			c.log.Error("session open err: %s", err)
		}
	}):
	case <-time.After(time.Second * 10):
		if stream != nil {
			stream.Close()
		}
		return errors.New("timeout opening session")
	}

	if _, err := stream.Write([]byte(ctHandshakeRequest)); err != nil {
		return err
	}

	buf := make([]byte, len(ctHandshakeResponse))
	if _, err := stream.Read(buf); err != nil {
		return err
	}

	if string(buf) != ctHandshakeResponse {
		return fmt.Errorf("handshake aborted. got: %s", string(buf))
	}

	ct := newControl(stream)
	c.log.Debug("client has started successfully.")

	return c.listenControl(ct)
}

func (c *Client) listenControl(ct *control) error {
	for {
		var msg ControlMsg
		err := ct.dec.Decode(&msg)
		if err != nil {
			return fmt.Errorf("decode err: '%s'", err)
		}

		c.log.Debug("controlMsg: %+v", msg)

		switch msg.Action {
		case RequestClientSession:
			go func() {
				if err := c.proxy(msg.LocalPort); err != nil {
					fmt.Fprintf(os.Stderr, "proxy err: '%s'\n", err)
				}
			}()
		}
	}
}

func (c *Client) proxy(port string) error {
	conn, err := c.session.Open()
	if err != nil {
		return err
	}

	if port == "0" {
		port = "80"
	}

	localAddr := "127.0.0.1:" + port
	if c.localAddr != "" {
		localAddr = c.localAddr
	}

	local, err := newLocalDial(localAddr)
	if err != nil {
		return err
	}

	go func() {
		<-join(local, conn)
		conn.Close()
	}()

	return nil
}

func newLocalDial(addr string) (net.Conn, error) {
	c, err := net.Dial("tcp", addr)
	if err != nil {
		return nil, err
	}

	c.SetDeadline(time.Time{})
	return c, nil
}

func join(local, remote io.ReadWriteCloser) chan error {
	errc := make(chan error, 2)

	copy := func(dst io.Writer, src io.Reader) {
		_, err := io.Copy(dst, src)
		errc <- err
	}

	go copy(local, remote)
	go copy(remote, local)

	return errc
}
