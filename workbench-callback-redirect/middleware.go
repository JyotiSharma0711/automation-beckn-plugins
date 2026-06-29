package callbackredirect

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/beckn-one/beckn-onix/pkg/log"
	"github.com/redis/go-redis/v9"
)

// Key prefixes and TTL form a cross-service contract with the automation-frontend
// backend and the Node api-service callbackController — keep in sync.
//
//	redirection_url:{transactionId} -> full workbench URL (written by frontend backend)
//	form_completed:{transactionId}   -> completion payload  (written here)
//
// Both are keyed by transaction_id, which the callback now receives directly as a
// query param — so no path/session reconstruction is needed.
const (
	formCompletedPrefix  = "form_completed"
	redirectionURLPrefix = "redirection_url"
	completionTTL        = 3600 * time.Second
)

// CallbackRedirect is a middleware for the public GET /callback. It reads
// transaction_id (and optional form_id) from the query string, looks up the
// workbench URL the frontend stored under redirection_url:{transactionId},
// writes form_completed:{transactionId}, and issues an immediate HTTP 302
// redirect back to the workbench. It never calls next.
type CallbackRedirect struct {
	redis *redis.Client
	ttl   time.Duration
}

// New initialises the middleware from the plugin config map.
// Required config key: "addr" — Redis address (e.g. "localhost:6379").
// Optional env vars:   REDIS_PASSWORD, REDIS_USERNAME
func New(ctx context.Context, config map[string]string) (func(http.Handler) http.Handler, error) {
	addr := config["addr"]
	if addr == "" {
		return nil, fmt.Errorf("callback-redirect: 'addr' (Redis address) is required in config")
	}

	client := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: os.Getenv("REDIS_PASSWORD"),
		Username: os.Getenv("REDIS_USERNAME"),
		DB:       0,
	})
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("callback-redirect: failed to connect to Redis at %s: %w", addr, err)
	}
	log.Infof(ctx, "callback-redirect: Redis connection established at %s", addr)

	m := &CallbackRedirect{redis: client, ttl: completionTTL}
	return m.handler, nil
}

func (m *CallbackRedirect) handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		// 0. GET-only: the callback is browser-navigated, so reject any other method.
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}

		// 1. transaction_id (and optional form_id) come in directly as query params.
		query := r.URL.Query()
		transactionID := query.Get("transaction_id")
		if transactionID == "" {
			http.Error(w, "transaction_id query param is required.", http.StatusBadRequest)
			return
		}
		formID := query.Get("form_id")

		// 2. Look up the stored workbench URL, keyed by transaction_id.
		redirectURL, err := m.redis.Get(ctx, fmt.Sprintf("%s:%s", redirectionURLPrefix, transactionID)).Result()
		if err != nil || redirectURL == "" {
			log.Warnf(ctx, "callback-redirect: no redirection URL for transaction %s", transactionID)
			http.Error(w, "No redirection URL found for this transaction.", http.StatusNotFound)
			return
		}

		// 3. Reaching the callback means completion; only explicit success=false fails.
		success := query.Get("success") != "false"

		// 4. Write form_completed:{transactionId} (form_id is carried in the payload).
		body, mErr := json.Marshal(map[string]interface{}{
			"completed": true,
			"success":   success,
			"form_id":   formID,
			"message":   "",
			"timestamp": time.Now().UTC().Format(time.RFC3339),
		})
		if mErr != nil {
			http.Error(w, "Error processing callback.", http.StatusInternalServerError)
			return
		}
		if err := m.redis.Set(ctx, fmt.Sprintf("%s:%s", formCompletedPrefix, transactionID), string(body), m.ttl).Err(); err != nil {
			log.Errorf(ctx, err, "callback-redirect: failed to write completion for transaction %s", transactionID)
			http.Error(w, "Error processing callback.", http.StatusInternalServerError)
			return
		}

		log.Infof(ctx, "callback-redirect: completion set, redirecting transaction %s -> %s", transactionID, redirectURL)

		// 6. Immediate HTTP 302 — do NOT call next; the request ends here.
		http.Redirect(w, r, redirectURL, http.StatusFound)
	})
}
