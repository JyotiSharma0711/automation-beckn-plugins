package networkobservability

import (
	"bufio"
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/beckn-one/beckn-onix/pkg/log"
	httprequestremap "github.com/extedcouD/HttpRequestRemapper"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/wrapperspb"
	"gopkg.in/yaml.v3"
)

type Config struct {
	Transport       string
	AuditURL        string
	AuditMethod     string
	Async           bool
	Timeout         time.Duration
	GRPCTarget      string
	GRPCInsecure    bool
	GRPCTimeout     time.Duration
	GRPCMethod      string
	QueueSize       int
	WorkerCount     int
	MaxBodyBytes    int64
	AuditHeaders    map[string]string
	BearerToken     string
	GRPCHeaders     map[string]string
	GRPCBearerToken string
	DropOnQueueFull bool
	RemapTemplate   any
}

// FileConfig is the YAML configuration schema.
// This keeps configuration structured (no JSON-in-string for remap).
type FileConfig struct {
	Transport       string            `yaml:"transport"`
	AuditURL        string            `yaml:"audit_url"`
	AuditMethod     string            `yaml:"audit_method"`
	Async           *bool             `yaml:"async"`
	TimeoutMs       *int              `yaml:"timeout_ms"`
	GRPCTarget      string            `yaml:"grpc_target"`
	GRPCInsecure    *bool             `yaml:"grpc_insecure"`
	GRPCTimeoutMs   *int              `yaml:"grpc_timeout_ms"`
	GRPCMethod      string            `yaml:"grpc_method"`
	QueueSize       *int              `yaml:"queue_size"`
	WorkerCount     *int              `yaml:"worker_count"`
	MaxBodyBytes    *int64            `yaml:"max_body_bytes"`
	Remap           any               `yaml:"remap"`
	DropOnQueueFull *bool             `yaml:"drop_on_queue_full"`
	AuditHeaders    map[string]string `yaml:"audit_headers"`
	BearerToken     string            `yaml:"audit_bearer_token"`
	GRPCHeaders     map[string]string `yaml:"grpc_headers"`
	GRPCBearerToken string            `yaml:"grpc_bearer_token"`
}

// NewNetworkObservabilityMiddleware loads plugin configuration from a YAML file.
func NewNetworkObservabilityMiddleware(ctx context.Context, configPath string) (func(http.Handler) http.Handler, error) {
	parsed, err := parseConfigFile(configPath)
	if err != nil {
		return nil, err
	}
	return newMiddlewareFromConfig(ctx, parsed)
}

func newMiddlewareFromConfig(ctx context.Context, parsed Config) (func(http.Handler) http.Handler, error) {
	transport := strings.ToLower(strings.TrimSpace(parsed.Transport))
	if transport == "" {
		transport = "http"
	}

	var sender auditSender
	switch transport {
	case "http":
		if strings.TrimSpace(parsed.AuditURL) == "" {
			log.Warnf(ctx, "network-observability: audit_url is empty; middleware is a no-op")
			return func(next http.Handler) http.Handler { return next }, nil
		}
		auditURL, err := url.Parse(parsed.AuditURL)
		if err != nil {
			return nil, err
		}
		headers := cloneStringMap(parsed.AuditHeaders)
		if parsed.BearerToken != "" {
			setAuthorizationIfMissing(headers, "Bearer "+parsed.BearerToken)
		}
		client := &http.Client{Timeout: parsed.Timeout}
		sender = &httpAuditSender{client: client, url: auditURL.String(), method: parsed.AuditMethod, headers: headers}
	case "grpc":
		if strings.TrimSpace(parsed.GRPCTarget) == "" {
			log.Warnf(ctx, "network-observability: grpc_target is empty; middleware is a no-op")
			return func(next http.Handler) http.Handler { return next }, nil
		}
		dialCtx := ctx
		if dialCtx == nil {
			dialCtx = context.Background()
		}
		if parsed.GRPCTimeout > 0 {
			var cancel context.CancelFunc
			dialCtx, cancel = context.WithTimeout(dialCtx, parsed.GRPCTimeout)
			defer cancel()
		}
		opts := []grpc.DialOption{grpc.WithBlock()}
		if parsed.GRPCInsecure {
			opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
		} else {
			opts = append(opts, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{})))
		}
		conn, err := grpc.DialContext(dialCtx, parsed.GRPCTarget, opts...)
		if err != nil {
			return nil, fmt.Errorf("network-observability: failed to dial grpc_target %s: %w", parsed.GRPCTarget, err)
		}
		headers := cloneStringMap(parsed.GRPCHeaders)
		if parsed.GRPCBearerToken != "" {
			setAuthorizationIfMissing(headers, "Bearer "+parsed.GRPCBearerToken)
		}
		sender = &grpcAuditSender{conn: conn, timeout: parsed.GRPCTimeout, headers: headers, method: parsed.GRPCMethod}
	default:
		return nil, fmt.Errorf("network-observability: unsupported transport %q (allowed: http, grpc)", parsed.Transport)
	}

	dispatcher := newAuditDispatcher(ctx, sender, parsed.QueueSize, parsed.WorkerCount, parsed.DropOnQueueFull)

	remapTemplate := parsed.RemapTemplate
	if remapTemplate == nil {
		remapTemplate = map[string]any{}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			log.Infof(r.Context(),"Running network-observability middleware for %s %s", r.Method, r.URL.Path)
			requestUUID, uuidErr := uuidV4()
			if uuidErr != nil {
				log.Errorf(r.Context(), uuidErr, "network-observability: failed generating uuid")
				requestUUID = ""
			}

			_, reqBodyBytes, _ := captureRequestBody(r, parsed.MaxBodyBytes)

			crw := newCaptureResponseWriter(w, parsed.MaxBodyBytes)
			start := time.Now()
			next.ServeHTTP(crw, r)
			durationMs := time.Since(start).Milliseconds()

			resBodyBytes, resTruncated := crw.bodyBytes()

			// A proxying module (actAsProxy) streams the upstream NP's response through
			// this writer verbatim, Content-Encoding and all: ReverseProxy forwards the
			// caller's Accept-Encoding, which switches off the transport's transparent
			// decompression. Decode before parsing, or a compressed reply is recorded as {}.
			reqBodyBytes = decodeBody(r.Context(), reqBodyBytes, r.Header.Get("Content-Encoding"), false)
			resBodyBytes = decodeBody(r.Context(), resBodyBytes, crw.Header().Get("Content-Encoding"), resTruncated)

			requestBody := parseJSONObjectOrEmpty(reqBodyBytes)
			responseBody := parseJSONObjectOrEmpty(resBodyBytes)
			if len(resBodyBytes) > 0 && len(responseBody) == 0 {
				log.Warnf(r.Context(), "network-observability: response body (%d bytes, content-encoding=%q) is not a JSON object; recording it as empty",
					len(resBodyBytes), crw.Header().Get("Content-Encoding"))
			}
			headersFirst, headersAll := headerMaps(r.Header)
			cookies := cookieMap(r)
			sid := ""
			for _, c := range r.Cookies() {
				if c != nil && c.Name == "sid" {
					sid = c.Value
					break
				}
			}

			remapInput := map[string]any{
				"requestBody":  requestBody,
				"responseBody": responseBody,
				"ctx": map[string]any{
					"ts":          time.Now().UTC().Format(time.RFC3339Nano),
					"duration_ms": durationMs,
					"uuid":        requestUUID,
					"method":      r.Method,
					"path":        r.URL.Path,
					"status":      crw.StatusCode(),
					"sid":         sid,
					"headers":     headersFirst,
					"headers_all": headersAll,
					"cookies":     cookies,
				},
			}

			additionalData := applyRemap(remapInput, remapTemplate, requestUUID)
			payload := map[string]any{
				"requestBody":    requestBody,
				"responseBody":   responseBody,
				"additionalData": additionalData,
			}

			body, marshalErr := json.Marshal(payload)
			if marshalErr != nil {
				log.Errorf(r.Context(), marshalErr, "network-observability: failed to marshal audit payload")
				return
			}

			if parsed.Async {
				// Async sends may happen after the HTTP request finishes; avoid using a
				// cancelable request context so audit dispatch isn't aborted.
				dispatcher.enqueue(context.WithoutCancel(r.Context()), body)
				return
			}
			log.Infof(r.Context(),"network-observability: sending audit event synchronously")
			dispatcher.sendNow(r.Context(), body)
		})
	}, nil
}

func parseConfigFile(configPath string) (Config, error) {
	configPath = strings.TrimSpace(configPath)
	if configPath == "" {
		return Config{}, fmt.Errorf("network-observability: config path is empty")
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		return Config{}, fmt.Errorf("network-observability: failed to read config file at %s: %w", configPath, err)
	}

	var fileCfg FileConfig
	if err := yaml.Unmarshal(data, &fileCfg); err != nil {
		return Config{}, fmt.Errorf("network-observability: failed to parse YAML: %w", err)
	}

	transport := strings.ToLower(strings.TrimSpace(fileCfg.Transport))
	grpcTarget := strings.TrimSpace(fileCfg.GRPCTarget)
	if transport == "" {
		if grpcTarget != "" {
			transport = "grpc"
		} else {
			transport = "http"
		}
	}

	grpcMethod := strings.TrimSpace(fileCfg.GRPCMethod)
	if grpcMethod == "" {
		grpcMethod = "/beckn.audit.v1.AuditService/LogEvent"
	}

	auditMethod := strings.ToUpper(strings.TrimSpace(fileCfg.AuditMethod))
	if auditMethod == "" {
		auditMethod = http.MethodPost
	}
	if transport == "http" {
		if auditMethod != http.MethodPost && auditMethod != http.MethodPut {
			return Config{}, errors.New("network-observability: unsupported audit_method (allowed: POST, PUT)")
		}
	}

	async := true
	if fileCfg.Async != nil {
		async = *fileCfg.Async
	}
	timeoutMs := 5000
	if fileCfg.TimeoutMs != nil && *fileCfg.TimeoutMs > 0 {
		timeoutMs = *fileCfg.TimeoutMs
	}
	grpcTimeoutMs := timeoutMs
	if fileCfg.GRPCTimeoutMs != nil && *fileCfg.GRPCTimeoutMs > 0 {
		grpcTimeoutMs = *fileCfg.GRPCTimeoutMs
	}
	queueSize := 1000
	if fileCfg.QueueSize != nil && *fileCfg.QueueSize > 0 {
		queueSize = *fileCfg.QueueSize
	}
	workerCount := 2
	if fileCfg.WorkerCount != nil && *fileCfg.WorkerCount > 0 {
		workerCount = *fileCfg.WorkerCount
	}
	maxBodyBytes := int64(1024 * 1024)
	if fileCfg.MaxBodyBytes != nil {
		maxBodyBytes = *fileCfg.MaxBodyBytes
		if maxBodyBytes < 0 {
			maxBodyBytes = 0
		}
	}
	dropOnQueueFull := true
	if fileCfg.DropOnQueueFull != nil {
		dropOnQueueFull = *fileCfg.DropOnQueueFull
	}

	grpcInsecure := false
	if fileCfg.GRPCInsecure != nil {
		grpcInsecure = *fileCfg.GRPCInsecure
	}

	return Config{
		Transport:       transport,
		AuditURL:        strings.TrimSpace(fileCfg.AuditURL),
		AuditMethod:     auditMethod,
		Async:           async,
		Timeout:         time.Duration(timeoutMs) * time.Millisecond,
		GRPCTarget:      grpcTarget,
		GRPCInsecure:    grpcInsecure,
		GRPCTimeout:     time.Duration(grpcTimeoutMs) * time.Millisecond,
		GRPCMethod:      grpcMethod,
		QueueSize:       queueSize,
		WorkerCount:     workerCount,
		MaxBodyBytes:    maxBodyBytes,
		DropOnQueueFull: dropOnQueueFull,
		AuditHeaders:    fileCfg.AuditHeaders,
		BearerToken:     strings.TrimSpace(fileCfg.BearerToken),
		GRPCHeaders:     fileCfg.GRPCHeaders,
		GRPCBearerToken: strings.TrimSpace(fileCfg.GRPCBearerToken),
		RemapTemplate:   fileCfg.Remap,
	}, nil
}

func captureRequestBody(r *http.Request, maxBytes int64) (bool, []byte, bool) {
	return httprequestremap.CaptureRequestBody(r, maxBytes)
}

type captureResponseWriter struct {
	http.ResponseWriter
	status    int
	buf       bytes.Buffer
	maxBytes  int64
	truncated bool
}

func newCaptureResponseWriter(w http.ResponseWriter, maxBytes int64) *captureResponseWriter {
	return &captureResponseWriter{ResponseWriter: w, status: 0, maxBytes: maxBytes}
}

func (w *captureResponseWriter) WriteHeader(statusCode int) {
	if w.status == 0 {
		w.status = statusCode
	}
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *captureResponseWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if w.maxBytes > 0 && !w.truncated {
		remaining := w.maxBytes - int64(w.buf.Len())
		if remaining > 0 {
			if int64(len(p)) <= remaining {
				_, _ = w.buf.Write(p)
			} else {
				_, _ = w.buf.Write(p[:remaining])
				w.truncated = true
			}
		} else {
			w.truncated = true
		}
	}
	return w.ResponseWriter.Write(p)
}

func (w *captureResponseWriter) bodyBytes() ([]byte, bool) {
	return w.buf.Bytes(), w.truncated
}

func (w *captureResponseWriter) StatusCode() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

func (w *captureResponseWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *captureResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	return h.Hijack()
}

func (w *captureResponseWriter) Push(target string, opts *http.PushOptions) error {
	p, ok := w.ResponseWriter.(http.Pusher)
	if !ok {
		return http.ErrNotSupported
	}
	return p.Push(target, opts)
}

func parseJSONObjectOrEmpty(b []byte) map[string]any {
	return httprequestremap.ParseJSONObjectOrEmpty(b)
}

// decodeBody decompresses b according to its Content-Encoding so the audit event
// carries the plaintext payload. Anything it cannot decode is returned unchanged —
// auditing must never alter what the client actually sent or received.
// truncated marks a body already cut short by MaxBodyBytes: the compressed stream is
// then incomplete, so a partial decode is the best that can be done.
func decodeBody(ctx context.Context, b []byte, contentEncoding string, truncated bool) []byte {
	if len(b) == 0 {
		return b
	}
	// Content-Encoding may list several codings; the last applied is the outermost.
	codings := strings.Split(strings.ToLower(contentEncoding), ",")
	encoding := strings.TrimSpace(codings[len(codings)-1])

	var (
		out []byte
		err error
	)
	switch encoding {
	case "", "identity":
		return b
	case "gzip", "x-gzip":
		out, err = readAllFrom(func() (io.ReadCloser, error) { return gzip.NewReader(bytes.NewReader(b)) })
	case "br":
		out, err = readAllFrom(func() (io.ReadCloser, error) {
			return io.NopCloser(brotli.NewReader(bytes.NewReader(b))), nil
		})
	case "deflate":
		// "deflate" is zlib-wrapped per RFC 7230; some servers send raw deflate instead.
		out, err = readAllFrom(func() (io.ReadCloser, error) { return zlib.NewReader(bytes.NewReader(b)) })
		if err != nil {
			out, err = readAllFrom(func() (io.ReadCloser, error) {
				return flate.NewReader(bytes.NewReader(b)), nil
			})
		}
	default:
		log.Warnf(ctx, "network-observability: unsupported Content-Encoding %q; recording body as-is", encoding)
		return b
	}

	// A truncated stream always errors out, but whatever decoded before the cut is still
	// worth recording; only fall back to the raw bytes when nothing came out.
	if err != nil && !(truncated && len(out) > 0) {
		log.Warnf(ctx, "network-observability: failed to decode %s body: %v; recording body as-is", encoding, err)
		return b
	}
	return out
}

func readAllFrom(open func() (io.ReadCloser, error)) ([]byte, error) {
	r, err := open()
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}

type auditDispatcher struct {
	sender          auditSender
	ch              chan auditJob
	workerCount     int
	dropOnQueueFull bool
	startOnce       sync.Once
	baseCtx         context.Context
}

type auditJob struct {
	ctx  context.Context
	body []byte
}

type auditSender interface {
	Send(ctx context.Context, body []byte) error
}

type httpAuditSender struct {
	client  *http.Client
	url     string
	method  string
	headers map[string]string
}

func (s *httpAuditSender) Send(ctx context.Context, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, s.method, s.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range s.headers {
		if strings.TrimSpace(k) == "" {
			continue
		}
		req.Header.Set(k, v)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Warnf(ctx, "network-observability: audit dispatch got status %d", resp.StatusCode)
	}
	return nil
}

type grpcAuditSender struct {
	conn    *grpc.ClientConn
	timeout time.Duration
	headers map[string]string
	method  string
}

func (s *grpcAuditSender) Send(ctx context.Context, body []byte) error {
	callCtx := ctx
	if s.timeout > 0 {
		var cancel context.CancelFunc
		callCtx, cancel = context.WithTimeout(ctx, s.timeout)
		defer cancel()
	}
	md := metadata.MD{}
	for k, v := range s.headers {
		lk := strings.ToLower(strings.TrimSpace(k))
		if lk == "" {
			continue
		}
		md.Append(lk, v)
	}
	if len(md) > 0 {
		callCtx = metadata.NewOutgoingContext(callCtx, md)
	}

	fullMethod := strings.TrimSpace(s.method)
	if fullMethod == "" {
		fullMethod = "/beckn.audit.v1.AuditService/LogEvent"
	}

	req := wrapperspb.Bytes(body)
	res := &emptypb.Empty{}
	return s.conn.Invoke(callCtx, fullMethod, req, res)
}

func newAuditDispatcher(baseCtx context.Context, sender auditSender, queueSize, workerCount int, dropOnQueueFull bool) *auditDispatcher {
	return &auditDispatcher{
		sender:          sender,
		ch:              make(chan auditJob, queueSize),
		workerCount:     workerCount,
		dropOnQueueFull: dropOnQueueFull,
		baseCtx:         baseCtx,
	}
}

func (d *auditDispatcher) start() {
	d.startOnce.Do(func() {
		for i := 0; i < d.workerCount; i++ {
			go d.worker()
		}
	})
}

func (d *auditDispatcher) enqueue(ctx context.Context, body []byte) {
	d.start()
	job := auditJob{ctx: ctx, body: body}
	select {
	case d.ch <- job:
	default:
		if !d.dropOnQueueFull {
			d.ch <- job
			return
		}
		log.Warnf(ctx, "network-observability: audit queue full; dropping event")
	}
}

func (d *auditDispatcher) worker() {
	for job := range d.ch {
		d.sendNow(job.ctx, job.body)
	}
}

func (d *auditDispatcher) sendNow(ctx context.Context, body []byte) {
	requestCtx := ctx
	if requestCtx == nil {
		requestCtx = d.baseCtx
	}
	if err := d.sender.Send(requestCtx, body); err != nil {
		log.Errorf(requestCtx, err, "network-observability: audit dispatch failed")
	}else{
		log.Infof(requestCtx, "network-observability: audit event dispatched successfully")
	}
}

func cloneStringMap(in map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range in {
		out[k] = v
	}
	return out
}

func setAuthorizationIfMissing(headers map[string]string, value string) {
	for k := range headers {
		if strings.EqualFold(strings.TrimSpace(k), "authorization") {
			return
		}
	}
	headers["Authorization"] = value
}

func headerMaps(h http.Header) (map[string]any, map[string]any) {
	return httprequestremap.HeaderMaps(h)
}

func cookieMap(r *http.Request) map[string]any {
	return httprequestremap.CookieMap(r)
}

func applyRemap(root any, template any, requestUUID string) any {
	return httprequestremap.ApplyTemplate(root, template, httprequestremap.Builtins{UUID: requestUUID})
}

func evalJSONPath(root any, expr string) any {
	return httprequestremap.EvalJSONPath(root, expr)
}

func uuidV4() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80

	buf := make([]byte, 36)
	hex.Encode(buf[0:8], b[0:4])
	buf[8] = '-'
	hex.Encode(buf[9:13], b[4:6])
	buf[13] = '-'
	hex.Encode(buf[14:18], b[6:8])
	buf[18] = '-'
	hex.Encode(buf[19:23], b[8:10])
	buf[23] = '-'
	hex.Encode(buf[24:36], b[10:16])
	return string(buf), nil
}
