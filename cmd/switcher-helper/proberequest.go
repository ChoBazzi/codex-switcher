package main

import (
	"errors"
	"io"
	"net"
	"net/http"
	"time"
)

// Bound only inbound headers/body. A streaming model response keeps its own
// existing timeout; a slow upload must not hold the activity lease indefinitely.
func probeHTTPServer(handler http.Handler) *http.Server {
	return &http.Server{ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, Handler: handler}
}

func readProbeBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	defer r.Body.Close()
	return io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<20))
}

func probeBodyRejection(err error) (int, string) {
	var timeout net.Error
	var oversized *http.MaxBytesError
	switch {
	case errors.As(err, &timeout) && timeout.Timeout():
		return http.StatusRequestTimeout, "request_body_timeout"
	case errors.As(err, &oversized):
		return http.StatusRequestEntityTooLarge, "request_too_large"
	default:
		return http.StatusBadRequest, "request_unreadable"
	}
}

const probeAuxiliaryLimit = 128

func probeAuxiliaryAdmission(failed, checkpointFailed bool, count int) string {
	if failed || checkpointFailed {
		return "probe_auxiliary_unavailable"
	}
	if count >= probeAuxiliaryLimit {
		return "auxiliary_capacity_reached"
	}
	return ""
}
