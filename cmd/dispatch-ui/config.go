package main

import (
	"bufio"
	"os"
	"strings"
)

// DispatchConfig holds the parsed configuration from dispatch.conf and .env.
type DispatchConfig struct {
	PlansDir            string
	ReportsDir          string
	LogFile             string
	DefaultModel        string
	DefaultAuthTokenEnv string
	DefaultBaseURL      string
	DefaultMaxRuntime   string
	ClaudeBin           string
	ListenAddr          string
	PlanningModel       string
	PlanningAPIKeyEnv   string
	PlanningBaseURL     string
	EnvVars             map[string]string // from .env
}

// loadDispatchConfig parses dispatch.conf and .env, returns a populated DispatchConfig.
func loadDispatchConfig(confPath, envPath string) (*DispatchConfig, error) {
	cfg := &DispatchConfig{
		// Defaults
		ListenAddr:        "127.0.0.1:8090",
		PlanningModel:     "claude-opus-4-6",
		PlanningAPIKeyEnv: "ANTHROPIC_API_KEY",
		EnvVars:           make(map[string]string),
	}

	// Parse dispatch.conf (KEY="value" format)
	err := parseShellFile(confPath, func(k, v string) {
		switch k {
		case "PLANS_DIR":
			cfg.PlansDir = v
		case "REPORTS_DIR":
			cfg.ReportsDir = v
		case "LOG_FILE":
			cfg.LogFile = v
		case "DEFAULT_MODEL":
			cfg.DefaultModel = v
		case "DEFAULT_AUTH_TOKEN_ENV":
			cfg.DefaultAuthTokenEnv = v
		case "DEFAULT_BASE_URL":
			cfg.DefaultBaseURL = v
		case "DEFAULT_MAX_RUNTIME":
			cfg.DefaultMaxRuntime = v
		case "CLAUDE_BIN":
			cfg.ClaudeBin = v
		case "LISTEN_ADDR":
			cfg.ListenAddr = v
		case "PLANNING_MODEL":
			cfg.PlanningModel = v
		case "PLANNING_API_KEY_ENV":
			cfg.PlanningAPIKeyEnv = v
		case "PLANNING_BASE_URL":
			cfg.PlanningBaseURL = v
		}
	})
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}

	// Parse .env (export KEY="value" format)
	err = parseShellFile(envPath, func(k, v string) {
		cfg.EnvVars[k] = v
	})
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}

	return cfg, nil
}

// parseShellFile reads a shell-style key=value file (dispatch.conf or .env format),
// calling fn for each key-value pair found. Handles:
//   - KEY="value" (conf format)
//   - export KEY="value" (.env format)
//   - $HOME expansion in values
//   - Blank lines and # comments ignored
func parseShellFile(path string, fn func(k, v string)) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	home, _ := os.UserHomeDir()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Strip leading "export " if present
		line = strings.TrimPrefix(line, "export ")
		line = strings.TrimSpace(line)

		// Split on first '='
		idx := strings.Index(line, "=")
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])

		// Strip surrounding quotes
		if len(val) >= 2 {
			if (val[0] == '"' && val[len(val)-1] == '"') ||
				(val[0] == '\'' && val[len(val)-1] == '\'') {
				val = val[1 : len(val)-1]
			}
		}

		// Expand $HOME
		if home != "" {
			val = strings.ReplaceAll(val, "$HOME", home)
		}

		if key != "" {
			fn(key, val)
		}
	}
	return scanner.Err()
}
