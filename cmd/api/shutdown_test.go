package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"testing"
	"time"

	inboundhttp "github.com/claudioed/task-interleaving/internal/adapters/inbound/http"
)

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

func waitListening(t *testing.T, addr string) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if c, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", addr); err == nil {
			_ = c.Close()
			return
		}
	}
	t.Fatalf("%s never listened", addr)
}

// recordingWorker is a worker whose stop records its name in order.
func recordingWorker(name string, order *[]string) *worker {
	w := &worker{name: name, done: make(chan struct{})}
	w.stop = func() {
		*order = append(*order, name)
		close(w.done)
	}
	return w
}

// shutdownRig is a server whose workers record the order in which they are
// stopped.
type shutdownRig struct {
	srv       *http.Server
	readiness *inboundhttp.Readiness
	order     []string
	result    chan error
}

func startShutdownRig(ctx context.Context, t *testing.T) *shutdownRig {
	t.Helper()
	t.Setenv("SHUTDOWN_DRAIN_DELAY", "0")
	r := &shutdownRig{
		readiness: &inboundhttp.Readiness{},
		srv:       &http.Server{Addr: freeAddr(t), Handler: http.NewServeMux(), ReadHeaderTimeout: time.Second},
		result:    make(chan error, 1),
	}
	workers := []*worker{
		recordingWorker("fulfillment", &r.order), recordingWorker("layout", &r.order),
		recordingWorker("expiry", &r.order), recordingWorker("relay", &r.order),
	}
	go func() {
		r.result <- serveUntilSignal(ctx, discardLogger(), r.srv, r.readiness, workers)
	}()
	waitListening(t, r.srv.Addr)
	return r
}

func (r *shutdownRig) expectCleanShutdown(t *testing.T) {
	t.Helper()
	select {
	case err := <-r.result:
		if err != nil {
			t.Fatalf("shutdown error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serveUntilSignal did not return")
	}
	if r.readiness.Ready() {
		t.Fatal("/readyz must be flipped to not-ready on shutdown")
	}
	if strings.Join(r.order, ",") != "fulfillment,layout,expiry,relay" {
		t.Fatalf("stop order = %v, want the consumers first, the expiry worker next and the relay last", r.order)
	}
}

// The fleet shutdown order: SIGTERM flips /readyz, drains HTTP, then stops the
// workers in the order given -- the consumers first, then the expiry worker,
// which enqueues outbox rows, and the relay last. The test traps SIGTERM
// itself first so the signal can never kill the test binary before
// serveUntilSignal has registered.
func TestServeUntilSignalShutsDownInOrder(t *testing.T) {
	trap := make(chan os.Signal, 1)
	signal.Notify(trap, syscall.SIGTERM)
	defer signal.Stop(trap)

	rig := startShutdownRig(context.Background(), t)
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	rig.expectCleanShutdown(t)
}

// Cancelling the context given to run is the same shutdown (the in-process
// end-to-end tests stop the service that way).
func TestServeUntilSignalShutsDownWhenTheContextIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	rig := startShutdownRig(ctx, t)
	cancel()
	rig.expectCleanShutdown(t)
}

func TestServeUntilSignalReturnsAListenError(t *testing.T) {
	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	srv := &http.Server{Addr: l.Addr().String(), Handler: http.NewServeMux(), ReadHeaderTimeout: time.Second}
	var order []string
	err = serveUntilSignal(context.Background(), discardLogger(), srv, &inboundhttp.Readiness{},
		[]*worker{recordingWorker("expiry", &order), recordingWorker("relay", &order)})
	if err == nil || errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("want the listen error, got %v", err)
	}
	if strings.Join(order, ",") != "expiry,relay" {
		t.Fatalf("stopped = %v: the workers must be stopped when the server cannot listen", order)
	}
}
