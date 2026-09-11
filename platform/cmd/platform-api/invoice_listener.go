package main

import (
	"context"
	"net"
)

func startUnifiedListener(ctx context.Context, addr string, start func(context.Context) error) (net.Listener, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	listener, err := new(net.ListenConfig).Listen(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	if err = start(ctx); err != nil {
		listener.Close()
		return nil, err
	}
	return listener, nil
}
