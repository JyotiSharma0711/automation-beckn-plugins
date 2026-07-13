package networkobservability

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/andybalholm/brotli"
)

const ackJSON = `{"message":{"ack":{"status":"ACK"}}}`

// auditFor runs handler behind the real middleware and returns the audit payload it dispatched.
func auditFor(t *testing.T, handler http.Handler) map[string]any {
	t.Helper()
	got := make(chan []byte, 4)
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- b
	}))
	defer sink.Close()

	cfgPath := filepath.Join(t.TempDir(), "cfg.yaml")
	cfg := fmt.Sprintf("transport: http\naudit_url: %s\nasync: false\nremap:\n  status_code: $.ctx.status\n", sink.URL)
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	mw, err := NewNetworkObservabilityMiddleware(context.Background(), cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	body := `{"context":{"action":"search","transaction_id":"txn-1"},"message":{}}`
	r := httptest.NewRequest(http.MethodPost, "/api-service/ONDC:RET14/1.2.5/mock/search", bytes.NewBufferString(body))
	// what the mock service (axios) actually sends, per ngrok:
	r.Header.Set("Accept-Encoding", "gzip, br")
	rec := httptest.NewRecorder()
	mw(handler).ServeHTTP(rec, r)

	select {
	case b := <-got:
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		t.Logf("client received: status=%d content-encoding=%q %d bytes",
			rec.Code, rec.Header().Get("Content-Encoding"), rec.Body.Len())
		return m
	default:
		t.Fatal("no audit event dispatched")
		return nil
	}
}

// proxyTo builds the /mock/* shape: stdHandler's actAsProxy path.
func proxyTo(t *testing.T, encoding string) http.Handler {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var out []byte
		switch encoding {
		case "br":
			var buf bytes.Buffer
			bw := brotli.NewWriter(&buf)
			_, _ = bw.Write([]byte(ackJSON))
			_ = bw.Close()
			out = buf.Bytes()
			w.Header().Set("Content-Encoding", "br")
		case "gzip":
			var buf bytes.Buffer
			gw := gzip.NewWriter(&buf)
			_, _ = gw.Write([]byte(ackJSON))
			_ = gw.Close()
			out = buf.Bytes()
			w.Header().Set("Content-Encoding", "gzip")
		default:
			out = []byte(ackJSON)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write(out)
	}))
	t.Cleanup(upstream.Close)
	target, _ := url.Parse(upstream.URL)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		(&httputil.ReverseProxy{Director: func(req *http.Request) {
			req.URL = target
			req.Host = target.Host
		}}).ServeHTTP(w, r)
	})
}

func TestMockProxy_RecordsResponse(t *testing.T) {
	for _, enc := range []string{"br", "gzip", "identity"} {
		t.Run(enc, func(t *testing.T) {
			p := auditFor(t, proxyTo(t, enc))
			rb, _ := json.Marshal(p["responseBody"])
			t.Logf("upstream Content-Encoding=%-8s -> recorded responseBody=%s", enc, rb)
			var want map[string]any
			_ = json.Unmarshal([]byte(ackJSON), &want)
			gotAck, _ := json.Marshal(p["responseBody"])
			wantAck, _ := json.Marshal(want)
			if string(gotAck) != string(wantAck) {
				t.Fatalf("recorded response is not the upstream ACK: got %s want %s", gotAck, wantAck)
			}
		})
	}
}

// The /buyer & /seller shape (actAsProxy:false) must keep working unchanged.
func TestLocalAck_RecordsResponse(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(ackJSON))
	})
	p := auditFor(t, h)
	rb, _ := json.Marshal(p["responseBody"])
	t.Logf("local ack -> recorded responseBody=%s", rb)
	if string(rb) != ackJSON {
		t.Fatalf("regression: got %s", rb)
	}
}
