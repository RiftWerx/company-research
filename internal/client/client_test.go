package client_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/riftwerx/company-research/internal/client"
)

func newTestClient(rate float64, burst int, timeout time.Duration) *client.Client {
	return client.New(client.Config{
		Rate:    rate,
		Burst:   burst,
		Timeout: timeout,
	})
}

func isContextError(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func TestClient(t *testing.T) {
	t.Run("should return a context error when context is cancelled while waiting for a rate-limit token", func(t *testing.T) {
		t.Parallel()

		// Arrange
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		// Rate of 0.001 req/s means a token won't be available for ~1000s.
		c := newTestClient(0.001, 1, 5*time.Second)

		// Exhaust the single burst token so the next call must wait.
		resp, err := c.Get(context.Background(), srv.URL)
		if err != nil {
			t.Fatalf("first request failed: %v", err)
		}
		resp.Body.Close()

		cancelCtx, cancel := context.WithCancel(context.Background())
		cancel() // cancel immediately — no token will be available

		// Act
		resp2, err := c.Get(cancelCtx, srv.URL)
		if resp2 != nil {
			resp2.Body.Close()
		}

		// Assert
		require.Error(t, err)
		assert.True(t, isContextError(err), "expected a context error, got: %v", err)
	})

	t.Run("should return a context error when context is cancelled during an in-flight request", func(t *testing.T) {
		t.Parallel()

		// Arrange
		started := make(chan struct{})
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			close(started)
			// Hold the connection open until the client disconnects.
			<-r.Context().Done()
		}))
		defer srv.Close()

		c := newTestClient(100, 1, 10*time.Second)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)

		// Act
		go func() {
			resp, err := c.Get(ctx, srv.URL)
			if resp != nil {
				resp.Body.Close()
			}
			done <- err
		}()

		<-started // wait until the request is in-flight before cancelling
		cancel()

		// Assert
		err := <-done
		assert.Error(t, err)
	})

	t.Run("should queue requests to stay within the configured rate", func(t *testing.T) {
		t.Parallel()

		// Arrange
		var count atomic.Int64
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			count.Add(1)
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		const reqPerSec = 10.0
		const n = 5
		c := newTestClient(reqPerSec, 1, 5*time.Second)

		// Act
		start := time.Now()
		for range n {
			resp, err := c.Get(context.Background(), srv.URL)
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			resp.Body.Close()
		}
		elapsed := time.Since(start)

		// Assert
		assert.Equal(t, int64(n), count.Load())
		// With burst=1 and rate=10/s, n=5 requests should take at least (n-1)/rate seconds.
		minExpected := time.Duration(float64(n-1)/reqPerSec*float64(time.Second)) - 50*time.Millisecond
		assert.GreaterOrEqual(t, elapsed, minExpected, "requests completed too fast — rate limiting may not be working")
	})

	t.Run("should cancel a request that exceeds the configured timeout", func(t *testing.T) {
		t.Parallel()

		// Arrange
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(500 * time.Millisecond)
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		c := newTestClient(100, 1, 50*time.Millisecond)

		// Act
		resp, err := c.Get(context.Background(), srv.URL)
		if resp != nil {
			resp.Body.Close()
		}

		// Assert
		assert.Error(t, err)
	})

	t.Run("should follow HTTP redirects", func(t *testing.T) {
		t.Parallel()

		// Arrange
		final := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer final.Close()

		redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, final.URL, http.StatusFound)
		}))
		defer redirector.Close()

		c := newTestClient(100, 1, 5*time.Second)

		// Act
		resp, err := c.Get(context.Background(), redirector.URL)

		// Assert
		require.NoError(t, err)
		if resp != nil {
			defer resp.Body.Close()
			assert.Equal(t, http.StatusOK, resp.StatusCode)
		}
	})

	t.Run("should send the configured User-Agent header", func(t *testing.T) {
		t.Parallel()

		// Arrange
		const wantUA = "company-research/0.1"
		var gotUA string

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotUA = r.Header.Get("User-Agent")
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		c := client.New(client.Config{
			Rate:      100,
			Burst:     1,
			Timeout:   5 * time.Second,
			UserAgent: wantUA,
		})

		// Act
		resp, err := c.Get(context.Background(), srv.URL)

		// Assert
		require.NoError(t, err)
		if resp != nil {
			resp.Body.Close()
		}
		assert.Equal(t, wantUA, gotUA)
	})
}
