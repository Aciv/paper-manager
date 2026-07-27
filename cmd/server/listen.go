package main

import (
	"context"
	"net"
	"time"

	"paperMaster/internal/logger"
)

func listenWithRetry(addr string, maxRetries int, baseDelay time.Duration) (net.Listener, error) {
	var lastErr error
	for i := 0; i < maxRetries; i++ {
		listener, err := net.Listen("tcp", addr)
		if err == nil {
			return listener, nil
		}
		lastErr = err
		if i < maxRetries-1 {
			delay := baseDelay * time.Duration(1<<uint(i))
			if delay > 3*time.Second {
				delay = 3 * time.Second
			}
			logger.Warn(context.Background(), "listen address unavailable; retrying", "address", addr, "delay", delay, "attempt", i+1, "max_retries", maxRetries, "error", err)
			time.Sleep(delay)
		}
	}
	return nil, lastErr
}
