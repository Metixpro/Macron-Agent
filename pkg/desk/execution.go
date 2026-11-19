package desk

import (
	"errors"
	"net/http"
)

type executionHost interface {
	SetExecutionMode(mode string) (string, error)
}

func (s *Server) executionAPI(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Mode string `json:"mode"`
	}
	if err := readJSON(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	eh, ok := s.host.(executionHost)
	if !ok {
		writeErr(w, http.StatusNotImplemented, errors.New("execution mode switching is not supported"))
		return
	}
	if _, err := eh.SetExecutionMode(in.Mode); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if s.hub != nil {
		s.hub.publish(map[string]any{"type": "info"})
	}
	writeJSON(w, s.info())
}
