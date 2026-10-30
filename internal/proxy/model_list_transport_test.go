package proxy

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"testing"
)

func TestDecodeModelListBody_ReadsPlainBody(t *testing.T) {
	payload := []byte(`{"models":{"gemini-2.5-pro":{}}}`)
	resp := &http.Response{
		Body: io.NopCloser(bytes.NewReader(payload)),
		Header: http.Header{
			"Content-Type": []string{"application/json"},
		},
	}

	body, err := decodeModelListBody(resp)
	if err != nil {
		t.Fatalf("decodeModelListBody: %v", err)
	}
	if !bytes.Equal(body, payload) {
		t.Errorf("body = %s, want %s", body, payload)
	}
}

func TestDecodeModelListBody_InflatesGzipResponse(t *testing.T) {
	payload := []byte(`{"models":{"gemini-2.5-pro":{"displayName":"Gemini 2.5 Pro"}}}`)

	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(payload); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}

	resp := &http.Response{
		Body: io.NopCloser(bytes.NewReader(compressed.Bytes())),
		Header: http.Header{
			"Content-Encoding": []string{"gzip"},
		},
	}

	body, err := decodeModelListBody(resp)
	if err != nil {
		t.Fatalf("decodeModelListBody: %v", err)
	}
	if !bytes.Equal(body, payload) {
		t.Errorf("decoded body = %s, want %s", body, payload)
	}
}

func TestDecodeModelListBody_RejectsEmptyResponse(t *testing.T) {
	if _, err := decodeModelListBody(nil); err == nil {
		t.Error("expected an error for a nil response")
	}
}
