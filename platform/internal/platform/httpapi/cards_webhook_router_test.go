package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// newWebhookTestRouter 只为把 chi 的路由参数喂给被测处理器。
func newWebhookTestRouter(h http.HandlerFunc) http.Handler {
	r := chi.NewRouter()
	r.Post("/webhooks/infini/{account}", h)
	return r
}
