package ui

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"mime"
	"net/http"
	"strconv"

	"github.com/andreabreu76/mem-engine/internal/config"
	"github.com/andreabreu76/mem-engine/internal/memory"
	"github.com/andreabreu76/mem-engine/internal/store"
)

const maxBodyBytes = 1 << 20

type Server struct {
	Store  *store.Store
	Memory *memory.Service
	Cfg    config.Config
	Token  string
	Port   int
	Log    *slog.Logger
}

type apiError struct {
	status int
	msg    string
}

func (e apiError) Error() string { return e.msg }

type errorJSON struct {
	Error string `json:"error"`
}

func NewToken() string {
	var b [32]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /api/projects", s.api(s.projects))
	mux.Handle("GET /api/feed", s.api(s.feed))
	mux.Handle("GET /api/search", s.api(s.search))
	mux.Handle("GET /api/item/{id}", s.api(s.item))
	mux.Handle("PUT /api/memory/{id}", s.api(s.updateMemory))
	mux.Handle("DELETE /api/memory/{id}", s.api(s.deleteMemory))
	mux.Handle("DELETE /api/observation/{id}", s.api(s.deleteObservation))
	mux.Handle("GET /api/health", s.api(s.health))
	return s.guard(mux)
}

func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Cache-Control", "no-store")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'")
		if !s.localOrigin(r.Host, "") {
			writeError(w, http.StatusForbidden, "forbidden host")
			return
		}
		if !s.validToken(r) {
			writeError(w, http.StatusUnauthorized, "missing or invalid token")
			return
		}
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
		default:
			if !s.localOrigin(r.Header.Get("Origin"), "http://") {
				writeError(w, http.StatusForbidden, "forbidden origin")
				return
			}
		}
		if r.Method == http.MethodPut {
			if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
				writeError(w, http.StatusUnsupportedMediaType, "content type must be application/json")
				return
			}
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		next.ServeHTTP(w, r)
	})
}

func (s *Server) localOrigin(value, scheme string) bool {
	port := ":" + strconv.Itoa(s.Port)
	return value == scheme+"127.0.0.1"+port || value == scheme+"localhost"+port
}

func (s *Server) validToken(r *http.Request) bool {
	given := r.Header.Get("X-Mem-Token")
	if given == "" {
		given = r.URL.Query().Get("token")
	}
	if s.Token == "" || len(given) != len(s.Token) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(given), []byte(s.Token)) == 1
}

func (s *Server) api(h func(http.ResponseWriter, *http.Request) (any, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v, err := h(w, r)
		if err == nil {
			writeJSON(w, http.StatusOK, v)
			return
		}
		var ae apiError
		if errors.As(err, &ae) {
			writeError(w, ae.status, ae.msg)
			return
		}
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		s.logger().Error("ui request failed", "method", r.Method, "path", r.URL.Path, "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
	})
}

func (s *Server) logger() *slog.Logger {
	if s.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return s.Log
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorJSON{Error: msg})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func badRequest(msg string) error {
	return apiError{status: http.StatusBadRequest, msg: msg}
}

var errNotFound = apiError{status: http.StatusNotFound, msg: "not found"}
