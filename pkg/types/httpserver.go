package types

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"time"
)

type TcpKeepAliveListener struct {
	*net.TCPListener
}

func (ln TcpKeepAliveListener) Accept() (net.Conn, error) {
	c, err := ln.AcceptTCP()
	if err != nil {
		return nil, err
	}
	c.SetKeepAlive(true)
	c.SetKeepAlivePeriod(3 * time.Minute)
	return c, nil
}

type HttpServer struct {
	handler  http.Handler
	listener net.Listener
	running  bool
}

func NewHttpServer(handler http.Handler) *HttpServer {
	return &HttpServer{
		handler: handler,
		running: false,
	}
}

func (s *HttpServer) ListenAndServe(address string, tlsConfig *tls.Config) error {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("Error listening on %s: %s", address, err)
	}
	s.listener = listener
	httpServer := &http.Server{Addr: address, Handler: s.handler}
	listener = TcpKeepAliveListener{s.listener.(*net.TCPListener)}
	if tlsConfig != nil {
		listener = tls.NewListener(listener, tlsConfig)
	}
	s.running = true
	return httpServer.Serve(listener)
}

func (s *HttpServer) Close() error {
	if s.listener != nil && s.running {
		err := s.listener.Close()
		if err != nil {
			return err
		}
		s.running = false
	}
	return nil
}

func (s *HttpServer) IsRunning() bool {
	return s.running
}

func ListenAndServe(handler http.Handler, addr string, tlsConfig *tls.Config) error {
	return NewHttpServer(handler).ListenAndServe(addr, tlsConfig)
}
