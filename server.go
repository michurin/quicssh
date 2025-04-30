package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"sync/atomic"
	"time"

	quic "github.com/quic-go/quic-go"
	cli "github.com/urfave/cli/v3"
)

func server(ctx context.Context, cmd *cli.Command) error { //nolint:funlen,cyclop // good reason for refactoring
	ctx, cancel := context.WithCancel(withLabel(ctx, "server"))
	defer cancel()

	raddr, err := net.ResolveTCPAddr("tcp", cmd.String("sshdaddr"))
	if err != nil {
		return er(ctx, err)
	}

	activeSessions := new(atomic.Int32)
	countDown := new(atomic.Int64) // int64 like time.Duration

	timeout, err := time.ParseDuration(cmd.String("idletimeout"))
	if err != nil {
		return er(ctx, err)
	}
	if timeout > 0 {
		logf(ctx, "Server runs with idle timeout: %v", timeout)
		go func() {
			for {
				time.Sleep(time.Second)
				if activeSessions.Load() == 0 {
					n := time.Duration(countDown.Add(int64(time.Second)))
					if n >= timeout {
						logf(ctx, "Timeout %v. Exiting", n)
						cancel()
						return
					}
					logf(ctx, "Timeout countdown: %v/%v", n, timeout)
				}
			}
		}()
	}

	config, err := tlsConfig(ctx)
	if err != nil {
		return err // already wrapped
	}

	// configure listener
	listener, err := quic.ListenAddr(cmd.String("bind"), config, nil)
	if err != nil {
		return er(ctx, err)
	}
	defer listener.Close()
	logf(ctx, "Listening at %q... (sshd addr: %q)", cmd.String("bind"), cmd.String("sshdaddr"))

	for {
		logf(ctx, "Accepting connection...")
		conn, err := listener.Accept(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return er(ctx, err)
			}
			logf(ctx, "Listener error (sleeping one second): %v", err)
			time.Sleep(time.Second)
			continue
		}

		countDown.Store(0)
		activeSessions.Add(1)
		go serverConnectionHandler(ctx, conn, raddr, activeSessions)
	}
}

func tlsConfig(ctx context.Context) (*tls.Config, error) {
	// generate TLS certificate
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, er(ctx, err)
	}
	template := x509.Certificate{SerialNumber: big.NewInt(1)}
	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return nil, er(ctx, err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, er(ctx, err)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{tlsCert},
		NextProtos:   []string{"quicssh"},
	}, nil
}

func serverConnectionHandler(ctx context.Context, conn quic.Connection, raddr *net.TCPAddr, activeSessions *atomic.Int32) { // TODO return error
	logf(ctx, "Handling session...")
	ctx, cancel := withCancelFromCtx(ctx, conn.Context()) // conn.Context() isn't linked with ctx from listener.Accept(ctx)
	defer cancel()
	defer activeSessions.Add(-1)
	defer func() {
		if err := conn.CloseWithError(0, "close"); err != nil {
			logf(ctx, "Session close error: %v", err)
		}
	}()
	for {
		stream, err := conn.AcceptStream(ctx)
		if err != nil {
			logf(ctx, "Session error: %v", err)
			break
		}
		go serverStreamHandler(ctx, stream, raddr)
	}
}

func serverStreamHandler(ctx context.Context, conn io.ReadWriteCloser, raddr *net.TCPAddr) { // TODO return error
	logf(ctx, "Handling stream...")
	defer conn.Close()

	rConn, err := net.DialTCP("tcp", nil, raddr)
	if err != nil {
		logf(ctx, "Dial error: %v", err)
		return
	}
	defer rConn.Close()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	c1 := readAndWrite(withLabel(ctx, "toSSHD"), conn, rConn)
	c2 := readAndWrite(withLabel(ctx, "fromSSHD"), rConn, conn)
	select {
	case err = <-c1:
	case err = <-c2:
	}
	if err != nil {
		logf(ctx, "readAndWrite error: %v", err)
		return
	}
	logf(ctx, "Piping finished")
}
