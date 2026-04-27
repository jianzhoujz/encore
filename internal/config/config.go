package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Config is the top-level configuration structure.
type Config struct {
	Server          ServerConfig              `json:"server"`
	Log             LogConfig                 `json:"log"`
	Retry           RetryConfig               `json:"retry"`
	ActiveProviders ActiveProvidersConfig     `json:"activeProviders"`
	Providers       map[string]ProviderConfig `json:"providers"`
}

// ServerConfig defines the local proxy server listen addresses.
// openaiPort is required; anthropicPort is optional (0 = disabled).
type ServerConfig struct {
	Host          string `json:"host"`
	OpenaiPort    int    `json:"openaiPort"`
	AnthropicPort int    `json:"anthropicPort"`
}

// LogConfig controls log levels for console and file output.
type LogConfig struct {
	ConsoleLevel string `json:"consoleLevel"`
	FileLevel    string `json:"fileLevel"`
}

// RetryConfig defines retry behavior for upstream requests.
type RetryConfig struct {
	MaxRetries    int `json:"maxRetries"`
	RetryInterval int `json:"retryInterval"` // milliseconds
}

// ActiveProvidersConfig maps each protocol to its active provider key.
// An empty string means that protocol is not enabled.
type ActiveProvidersConfig struct {
	OpenAI    string `json:"openai"`
	Anthropic string `json:"anthropic"`
}

// ProviderConfig describes an upstream AI API provider.
type ProviderConfig struct {
	Name         string `json:"name"`
	Protocol     string `json:"protocol"`   // "openai" | "anthropic"
	BaseURL      string `json:"baseUrl"`
	APIKey       string `json:"apiKey"`
	ModelsFile   string `json:"models"`     // optional: custom model list JSON filename
	OverrideModel string `json:"overrideModel"` // optional: force all requests to use this model name
}

// ConfigDir returns the path to the Encore config directory.
func ConfigDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "encore")
}

// ActiveProvider returns the provider config for the given protocol
// based on activeProviders mapping. Returns false if not configured.
func (c *Config) ActiveProvider(protocol string) (ProviderConfig, bool) {
	var key string
	switch protocol {
	case "openai":
		key = c.ActiveProviders.OpenAI
	case "anthropic":
		key = c.ActiveProviders.Anthropic
	default:
		return ProviderConfig{}, false
	}
	if key == "" {
		return ProviderConfig{}, false
	}
	p, ok := c.Providers[key]
	return p, ok
}

// Load reads and validates the config from ~/.config/encore/config.json.
func Load() (*Config, error) {
	configPath := filepath.Join(ConfigDir(), "config.json")

	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file %s: %w", configPath, err)
	}

	// Phase 1: validate all required fields are present in the raw JSON.
	if errs := validateRawJSON(data); len(errs) > 0 {
		return nil, fmt.Errorf("config validation failed:\n  - %s", strings.Join(errs, "\n  - "))
	}

	// Phase 2: unmarshal into struct.
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	// Phase 3: semantic validation on parsed values.
	if errs := validateConfig(&cfg); len(errs) > 0 {
		return nil, fmt.Errorf("config validation failed:\n  - %s", strings.Join(errs, "\n  - "))
	}

	return &cfg, nil
}

// validateRawJSON checks that all required fields are present in the raw JSON,
// regardless of their values. This ensures no field is accidentally omitted.
func validateRawJSON(data []byte) []string {
	var errors []string

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return []string{fmt.Sprintf("invalid JSON: %v", err)}
	}

	// Top-level fields
	topLevel := []string{"server", "log", "retry", "activeProviders", "providers"}
	for _, key := range topLevel {
		if _, ok := raw[key]; !ok {
			errors = append(errors, fmt.Sprintf("missing required field: %s", key))
		}
	}

	// server.*
	if serverRaw, ok := raw["server"]; ok {
		errors = append(errors, checkObjectKeys(serverRaw, "server", []string{"host", "openaiPort"})...)
	}

	// log.*
	if logRaw, ok := raw["log"]; ok {
		errors = append(errors, checkObjectKeys(logRaw, "log", []string{"consoleLevel", "fileLevel"})...)
	}

	// retry.*
	if retryRaw, ok := raw["retry"]; ok {
		errors = append(errors, checkObjectKeys(retryRaw, "retry", []string{"maxRetries", "retryInterval"})...)
	}

	// activeProviders.*
	if apRaw, ok := raw["activeProviders"]; ok {
		errors = append(errors, checkObjectKeys(apRaw, "activeProviders", []string{"openai", "anthropic"})...)
	}

	// providers.*
	if providersRaw, ok := raw["providers"]; ok {
		var providers map[string]json.RawMessage
		if err := json.Unmarshal(providersRaw, &providers); err != nil {
			errors = append(errors, "field 'providers' must be an object")
		} else {
			for key, providerRaw := range providers {
				prefix := fmt.Sprintf("providers.%s", key)
				errors = append(errors, checkObjectKeys(providerRaw, prefix, []string{"name", "protocol", "baseUrl", "apiKey"})...)
			}
		}
	}

	return errors
}

// checkObjectKeys verifies that the given JSON value is an object containing all required keys.
func checkObjectKeys(raw json.RawMessage, prefix string, keys []string) []string {
	var errors []string
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return []string{fmt.Sprintf("field '%s' must be an object", prefix)}
	}
	for _, key := range keys {
		if _, ok := obj[key]; !ok {
			errors = append(errors, fmt.Sprintf("missing required field: %s.%s", prefix, key))
		}
	}
	return errors
}

// validateConfig performs semantic validation on the parsed config struct.
func validateConfig(cfg *Config) []string {
	var errors []string

	// server
	if cfg.Server.Host == "" {
		errors = append(errors, "server.host cannot be empty")
	}
	if cfg.Server.OpenaiPort <= 0 || cfg.Server.OpenaiPort > 65535 {
		errors = append(errors, "server.openaiPort must be between 1 and 65535")
	}
	if cfg.Server.AnthropicPort < 0 || cfg.Server.AnthropicPort > 65535 {
		errors = append(errors, "server.anthropicPort must be between 0 and 65535 (0 = disabled)")
	}
	if cfg.Server.OpenaiPort == cfg.Server.AnthropicPort && cfg.Server.AnthropicPort != 0 {
		errors = append(errors, "server.openaiPort and server.anthropicPort must be different")
	}

	// log
	validLevels := map[string]bool{"verbose": true, "debug": true, "info": true, "error": true}
	if !validLevels[cfg.Log.ConsoleLevel] {
		errors = append(errors, fmt.Sprintf("log.consoleLevel must be one of: verbose, debug, info, error (got: %q)", cfg.Log.ConsoleLevel))
	}
	if !validLevels[cfg.Log.FileLevel] {
		errors = append(errors, fmt.Sprintf("log.fileLevel must be one of: verbose, debug, info, error (got: %q)", cfg.Log.FileLevel))
	}

	// retry
	if cfg.Retry.MaxRetries <= 0 {
		errors = append(errors, "retry.maxRetries must be a positive number")
	}
	if cfg.Retry.RetryInterval <= 0 {
		errors = append(errors, "retry.retryInterval must be a positive number (milliseconds)")
	}

	// activeProviders — at least one must be set
	ap := cfg.ActiveProviders
	if ap.OpenAI == "" && ap.Anthropic == "" {
		errors = append(errors, "activeProviders must have at least one non-empty provider (openai or anthropic)")
	}

	// activeProviders references must exist and match protocol
	if ap.OpenAI != "" {
		if p, ok := cfg.Providers[ap.OpenAI]; !ok {
			errors = append(errors, fmt.Sprintf("activeProviders.openai %q does not match any key in providers", ap.OpenAI))
		} else if p.Protocol != "openai" {
			errors = append(errors, fmt.Sprintf("activeProviders.openai %q points to a provider with protocol %q, expected \"openai\"", ap.OpenAI, p.Protocol))
		}
	}
	if ap.Anthropic != "" {
		if p, ok := cfg.Providers[ap.Anthropic]; !ok {
			errors = append(errors, fmt.Sprintf("activeProviders.anthropic %q does not match any key in providers", ap.Anthropic))
		} else if p.Protocol != "anthropic" {
			errors = append(errors, fmt.Sprintf("activeProviders.anthropic %q points to a provider with protocol %q, expected \"anthropic\"", ap.Anthropic, p.Protocol))
		}
	}

	// providers
	if len(cfg.Providers) == 0 {
		errors = append(errors, "providers must contain at least one provider")
	}

	// each provider
	validProtocols := map[string]bool{"openai": true, "anthropic": true}
	for key, p := range cfg.Providers {
		prefix := fmt.Sprintf("providers.%s", key)
		if p.Name == "" {
			errors = append(errors, fmt.Sprintf("%s.name cannot be empty", prefix))
		}
		if !validProtocols[p.Protocol] {
			errors = append(errors, fmt.Sprintf("%s.protocol must be one of: openai, anthropic (got: %q)", prefix, p.Protocol))
		}
		if p.BaseURL == "" {
			errors = append(errors, fmt.Sprintf("%s.baseUrl cannot be empty", prefix))
		}
		if p.APIKey == "" {
			errors = append(errors, fmt.Sprintf("%s.apiKey cannot be empty", prefix))
		}
	}

	return errors
}
