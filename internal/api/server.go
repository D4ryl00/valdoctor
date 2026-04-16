package api

import (
	"context"
	"errors"
	"net/http"

	storepkg "github.com/D4ryl00/valdoctor/internal/store"
)

type Server struct {
	Addr    string
	ChainID string
	Store   storepkg.Store

	httpServer *http.Server
}

func (s *Server) Run(ctx context.Context) error {
	if s.Addr == "" {
		return nil
	}

	handler := withCORS(s.routes())
	s.httpServer = &http.Server{
		Addr:    s.Addr,
		Handler: handler,
	}

	errCh := make(chan error, 1)
	go func() {
		err := s.httpServer.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		_ = s.httpServer.Shutdown(shutdownCtx)
		return <-errCh
	case err := <-errCh:
		return err
	}
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
