package server

import (
	"errors"
	"io"
	"net/http"

	"mqConnector/internal/sample"
)

// handleExtractSample takes either a multipart upload (file field) or a raw
// request body and returns the inferred format + every dot-path the sample
// contains. The 2024 prototype persisted the result into a Templates
// collection; the rewrite returns it directly so the editor can show a
// path-picker without any extra round-trip.
//
// POST /api/v1/samples/extract
//
// Accepts:
//   multipart/form-data with a `file` part, OR
//   any other Content-Type — the raw body is treated as the sample.
//
// Response: { "format": "json|xml|bytes", "root_tag": "...", "paths": [...] }
func (s *Server) handleExtractSample(w http.ResponseWriter, r *http.Request) {
	body, err := readSampleBody(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(body) == 0 {
		writeError(w, http.StatusBadRequest, "empty sample")
		return
	}
	result, err := sample.Extract(body)
	if err != nil {
		if errors.Is(err, sample.ErrTooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "sample too large")
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// readSampleBody handles both multipart-form uploads and raw bodies. The
// server-wide MaxBodyBytes middleware has already capped the request, so we
// don't need a second limit here — but we still bound the multipart
// in-memory portion to MaxSize to avoid copying a giant blob twice.
func readSampleBody(r *http.Request) ([]byte, error) {
	ct := r.Header.Get("Content-Type")
	if len(ct) >= len("multipart/form-data") && ct[:len("multipart/form-data")] == "multipart/form-data" {
		if err := r.ParseMultipartForm(sample.MaxSize); err != nil {
			return nil, err
		}
		f, _, err := r.FormFile("file")
		if err != nil {
			return nil, err
		}
		defer f.Close()
		return io.ReadAll(io.LimitReader(f, sample.MaxSize+1))
	}
	return io.ReadAll(io.LimitReader(r.Body, sample.MaxSize+1))
}
