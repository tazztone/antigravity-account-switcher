package proxy

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestNewBufferedRequestWithLimit_PreallocationAndLimits(t *testing.T) {
	t.Run("valid content length preallocates accurately", func(t *testing.T) {
		payload := []byte("hello world large payload test")
		req, err := http.NewRequest(http.MethodPost, "http://localhost", bytes.NewReader(payload))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		req.ContentLength = int64(len(payload))

		buffered, err := NewBufferedRequestWithLimit(req, 1024)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if string(buffered.Body) != string(payload) {
			t.Fatalf("expected body %q, got %q", string(payload), string(buffered.Body))
		}

		// Verify NewReader provides exact copy
		rc := buffered.NewReader()
		defer rc.Close()
		readBack, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("failed reading back: %v", err)
		}
		if string(readBack) != string(payload) {
			t.Fatalf("expected readBack %q, got %q", string(payload), string(readBack))
		}
	})

	t.Run("body exceeds maxBytes returns ErrRequestBodyTooLarge", func(t *testing.T) {
		payload := []byte(strings.Repeat("a", 100))
		req, err := http.NewRequest(http.MethodPost, "http://localhost", bytes.NewReader(payload))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		req.ContentLength = int64(len(payload))

		_, err = NewBufferedRequestWithLimit(req, 50)
		if !errors.Is(err, ErrRequestBodyTooLarge) {
			t.Fatalf("expected ErrRequestBodyTooLarge, got %v", err)
		}
	})

	t.Run("unknown or negative content length falls back to ReadAll", func(t *testing.T) {
		payload := []byte("chunked or unknown stream")
		req, err := http.NewRequest(http.MethodPost, "http://localhost", bytes.NewReader(payload))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		req.ContentLength = -1

		buffered, err := NewBufferedRequestWithLimit(req, 1024)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if string(buffered.Body) != string(payload) {
			t.Fatalf("expected body %q, got %q", string(payload), string(buffered.Body))
		}
	})

	t.Run("nil or empty body returns nil body without error", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodGet, "http://localhost", nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		buffered, err := NewBufferedRequestWithLimit(req, 1024)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if buffered.Body != nil {
			t.Fatalf("expected nil body, got %v", buffered.Body)
		}
		if buffered.NewReader() != http.NoBody {
			t.Fatalf("expected http.NoBody, got %v", buffered.NewReader())
		}
	})
}
