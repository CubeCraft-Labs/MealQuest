package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/jobs"
)

// maxJobBody bounds a POST /jobs request body.
const maxJobBody = 64 << 10

// jobRequest is the POST /jobs body. Payload is required; a job kind that
// takes no input is sent the empty object {}.
type jobRequest struct {
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
}

// jobCreated is the POST /jobs response.
type jobCreated struct {
	ID string `json:"id"`
}

// postJob serves POST /api/v1/jobs: it enqueues a job of a registered kind
// that is not service-only and returns 202 with its id, which
// GET /api/v1/jobs/{id} reports on.
func postJob(logger *slog.Logger, runner *jobs.Runner) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		req, err := decodeJobRequest(http.MaxBytesReader(w, r.Body, maxJobBody))
		if err != nil {
			writeError(w, r, logger, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		id, err := runner.Enqueue(r.Context(), req.Kind, req.Payload)
		switch {
		case errors.Is(err, jobs.ErrUnknownKind):
			writeError(w, r, logger, http.StatusBadRequest, "unknown_kind", "unknown job kind")
			return
		case errors.Is(err, jobs.ErrServiceOnly):
			writeError(w, r, logger, http.StatusBadRequest, "service_only_kind", "job kind is started only by its own endpoint")
			return
		case errors.Is(err, jobs.ErrInvalidPayload):
			writeError(w, r, logger, http.StatusBadRequest, "invalid_payload", "payload does not match the job kind")
			return
		case err != nil:
			logger.ErrorContext(r.Context(), "enqueue job", "kind", req.Kind, "error", err)
			writeError(w, r, logger, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		writeJSON(w, r, logger, http.StatusAccepted, jobCreated{ID: id})
	}
}

// decodeJobRequest reads a single JSON object with a non-empty kind and a
// payload, refusing unknown fields, an omitted or null payload and trailing
// data.
func decodeJobRequest(body io.Reader) (jobRequest, error) {
	data, err := io.ReadAll(body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return jobRequest{}, fmt.Errorf("request body exceeds %d bytes", tooLarge.Limit)
		}
		return jobRequest{}, errors.New("cannot read request body")
	}
	var req jobRequest
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return jobRequest{}, errors.New("request body must be a JSON object with kind and payload")
	}
	if _, err := dec.Token(); err != io.EOF {
		return jobRequest{}, errors.New("request body must be a single JSON object")
	}
	if req.Kind == "" {
		return jobRequest{}, errors.New("kind: required")
	}
	if req.Payload == nil {
		return jobRequest{}, errors.New("payload: required")
	}
	if bytes.Equal(req.Payload, []byte("null")) {
		return jobRequest{}, errors.New("payload: must not be null")
	}
	return req, nil
}

// getJob serves GET /api/v1/jobs/{id}: the job's id, kind, status, latest
// result and error.
func getJob(logger *slog.Logger, runner *jobs.Runner) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		job, err := runner.Get(r.Context(), chi.URLParam(r, "id"))
		if errors.Is(err, jobs.ErrNotFound) {
			writeError(w, r, logger, http.StatusNotFound, "not_found", "job not found")
			return
		}
		if err != nil {
			logger.ErrorContext(r.Context(), "get job", "error", err)
			writeError(w, r, logger, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		writeJSON(w, r, logger, http.StatusOK, job)
	}
}
