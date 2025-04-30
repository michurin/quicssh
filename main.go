package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path"
	"runtime"
	"runtime/debug"

	cli "github.com/urfave/cli/v3"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background()) // TODO: application context, good for graceful shutdown
	defer cancel()
	app := application()
	if err := app.Run(ctx, os.Args); err != nil {
		logf(ctx, "Error: %v", err)
	}
}

func application() *cli.Command {
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)
	return &cli.Command{
		Version: versionString(debug.ReadBuildInfo),
		Usage:   "Client and server parts to proxy SSH (TCP) over QUIC (UDP) transport",
		Commands: []*cli.Command{
			{
				Name: "server",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "bind", Value: "localhost:4242", Usage: "bind address"},
					&cli.StringFlag{Name: "sshdaddr", Value: "localhost:22", Usage: "target address of sshd"},
					&cli.StringFlag{Name: "idletimeout", Value: "0s", Usage: "exit on idle interval (10s, 2m, 1h)"},
				},
				Action: server,
			},
			{
				Name: "client",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "addr", Value: "localhost:4242", Usage: "address of server"},
					&cli.StringFlag{Name: "localaddr", Value: ":0", Usage: "source address of UDP packets"},
				},
				Action: client,
			},
		},
	}
}

func readAndWrite(ctx context.Context, r io.Reader, w io.Writer) <-chan error {
	c := make(chan error)
	go func() {
		defer close(c)

		buff := make([]byte, 8*1024)

		for {
			select {
			case <-ctx.Done():
				c <- er(ctx, ctx.Err())
				return
			default:
				nr, err := r.Read(buff)
				if err != nil {
					c <- er(ctx, err)
					return
				}
				if nr > 0 {
					_, err := io.Copy(w, bytes.NewReader(buff[:nr]))
					if err != nil {
						c <- er(ctx, err)
						return
					}
				}
			}
		}
	}()
	return c
}

func er(ctx context.Context, e error) error {
	_, f, l, _ := runtime.Caller(1)
	return fmt.Errorf("[%s] %s:%d: %w", label(ctx), path.Base(f), l, e)
}

func logf(ctx context.Context, format string, v ...any) {
	log.Printf("[%s] %s", label(ctx), fmt.Sprintf(format, v...))
}

type lableKeyT int

const lableKey = lableKeyT(0)

func withLabel(ctx context.Context, label string) context.Context {
	if parent, ok := ctx.Value(lableKey).(string); ok {
		label = parent + ">" + label
	}
	return context.WithValue(ctx, lableKey, label)
}

func label(ctx context.Context) string {
	label, _ := ctx.Value(lableKey).(string)
	if label == "" {
		return "main"
	}
	return label
}

func withCancelFromCtx(ctx, cancelCtx context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		select {
		case <-cancelCtx.Done():
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}

func versionString(buildInfo func() (*debug.BuildInfo, bool)) string {
	b, ok := buildInfo()
	if !ok {
		return "no_build_info"
	}
	m := b.Main
	s := m.Sum
	if len(s) == 0 {
		s = "(sum)"
	}
	return m.Path + "/" + s + "/" + m.Version + " (go version: " + b.GoVersion + ")"
}
