package proxy

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"

	"github.com/jianzhoujz/encore/internal/config"
)

// ---------------------------------------------------------------------------
// Custom model list override
// ---------------------------------------------------------------------------
//
// When a provider has "models" set in config.json, Encore intercepts GET
// /v1/models and returns a custom model list read from the configured JSON
// file instead of proxying to the upstream provider.
//
// The model list files live in ~/.config/encore/ by default.
//
// If a provider does not have "models" set, the request is proxied to the
// upstream provider as usual.

// handleModels intercepts GET /v1/models when the configured provider has a
// custom model list. Returns true if the request was handled (intercepted),
// false if it should be proxied normally.
func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) bool {
	if s.provider.ModelsFile == "" {
		return false
	}
	if r.URL.Path != "/v1/models" && r.URL.Path != "/v1/models/" {
		return false
	}
	if r.Method != http.MethodGet {
		return false
	}

	filePath := filepath.Join(config.ConfigDir(), s.provider.ModelsFile)

	data, err := os.ReadFile(filePath)
	if err != nil {
		s.logger.Error("Failed to read %s: %s", filePath, err)
		http.Error(w, "failed to read custom model list", http.StatusInternalServerError)
		return true
	}

	// Validate it's valid JSON before sending.
	if !json.Valid(data) {
		s.logger.Error("Invalid JSON in %s", filePath)
		http.Error(w, "custom model list contains invalid JSON", http.StatusInternalServerError)
		return true
	}

	s.logger.Info("-> GET /v1/models (serving custom model list from %s)", s.provider.ModelsFile)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(data)
	return true
}
