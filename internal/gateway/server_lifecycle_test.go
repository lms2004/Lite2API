package gateway

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestShutdownDeadlineClosesActiveRequestsAndListener(t *testing.T) {
	started, cancelled := make(chan struct{}), make(chan struct{})
	server := &http.Server{Handler: http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		close(cancelled)
	})}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	go func() { _ = server.Serve(listener) }()
	clientDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		client := &http.Client{Timeout: 3 * time.Second}
		if response, err := client.Get("http://" + listener.Addr().String()); err == nil {
			_ = response.Body.Close()
		}
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("request did not enter the server")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if err := shutdownWithin(ctx, server); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown error = %v, want deadline exceeded", err)
	}
	for name, done := range map[string]<-chan struct{}{"handler": cancelled, "client": clientDone} {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatalf("%s remained active after the shutdown deadline", name)
		}
	}
	// Prove the exact listening address has been released, rather than only
	// checking that a liveness request happens to fail.
	rebound, err := net.Listen("tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("listener was not released: %v", err)
	}
	_ = rebound.Close()
}

func TestShutdownGracePeriodCompletesActiveRequest(t *testing.T) {
	started, finish := make(chan struct{}), make(chan struct{})
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-finish
		_, _ = io.WriteString(w, "complete")
	})}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	go func() { _ = server.Serve(listener) }()
	clientResult := make(chan string, 1)
	go func() {
		client := &http.Client{Timeout: 3 * time.Second}
		response, err := client.Get("http://" + listener.Addr().String())
		if err != nil {
			clientResult <- err.Error()
			return
		}
		defer response.Body.Close()
		body, _ := io.ReadAll(response.Body)
		clientResult <- string(body)
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		close(finish)
		t.Fatal("request did not enter the server")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	shutdownResult := make(chan error, 1)
	go func() { shutdownResult <- shutdownWithin(ctx, server) }()
	close(finish)
	if got := <-clientResult; got != "complete" {
		t.Fatalf("graceful response = %q", got)
	}
	if err := <-shutdownResult; err != nil {
		t.Fatalf("graceful shutdown: %v", err)
	}
}
