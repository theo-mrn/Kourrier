package api

import (
	"encoding/json"
	"net/http"

	"github.com/theo-mrn/kourrier/internal/mail"
)

// Problem is an RFC 9457 "problem details" error response.
type Problem struct {
	Type      string            `json:"type"`
	Title     string            `json:"title"`
	Status    int               `json:"status"`
	Detail    string            `json:"detail,omitempty"`
	RequestID string            `json:"request_id,omitempty"`
	Errors    []mail.FieldError `json:"errors,omitempty"`
	// DeadLetterID is set when a rejected event was kept for replay.
	DeadLetterID string `json:"dead_letter_id,omitempty"`
}

const problemBase = "https://github.com/theo-mrn/kourrier/blob/main/docs/api.md#"

func writeProblem(w http.ResponseWriter, r *http.Request, status int, kind, detail string) {
	writeProblemDetails(w, r, &Problem{Type: problemBase + kind, Title: http.StatusText(status), Status: status, Detail: detail})
}

func writeProblemDetails(w http.ResponseWriter, r *http.Request, p *Problem) {
	p.RequestID = requestIDFrom(r.Context())
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(p.Status)
	_ = json.NewEncoder(w).Encode(p)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
