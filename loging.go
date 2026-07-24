package main

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sync"
)

type MyLogger struct {
	mu  sync.RWMutex
	buf bytes.Buffer
}

func (lb *MyLogger) Write(p []byte) (n int, err error) {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	return lb.buf.Write(p)
}

func (lb *MyLogger) String() string {
	lb.mu.RLock()
	defer lb.mu.RUnlock()
	return lb.buf.String()
}

var logBuf MyLogger

func Logger_init() {
	multiWriter := io.MultiWriter(os.Stderr, &logBuf)
	log.SetOutput(multiWriter)
	log.SetFlags(log.LstdFlags)
}

func Log_buffer(w http.ResponseWriter, r *http.Request) {

	if AUTH {
		jwt_claims := ExtractClaims(r)
		if jwt_claims == nil {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		if !VerifyAdmin(jwt_claims) {
			w.WriteHeader(http.StatusForbidden)
			return
		}
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprint(w, logBuf.String())
}
