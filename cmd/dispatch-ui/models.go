package main

// modelOption describes a selectable executor model and the provider routing
// (Anthropic-compatible base URL + API key env var) it needs. Empty BaseURL /
// APIKeyEnv means the dispatcher falls back to the config defaults and, absent
// those, subscription auth — used for first-party Anthropic models.
type modelOption struct {
	ID        string
	BaseURL   string
	APIKeyEnv string
}

// modelCatalog is the list of models offered in the UI dropdown, in display
// order. Routing here is applied at dispatch time when the plan doesn't set
// base_url / api_key_env explicitly, so file-dropped plans get it too.
var modelCatalog = []modelOption{
	{ID: "glm-5.3", BaseURL: "https://api.z.ai/api/anthropic", APIKeyEnv: "ZAI_API_KEY"},
	{ID: "glm-5.1", BaseURL: "https://api.z.ai/api/anthropic", APIKeyEnv: "ZAI_API_KEY"},
	{ID: "k3", BaseURL: "https://api.kimi.com/coding", APIKeyEnv: "KIMI_API_KEY"},
	{ID: "sonnet-4.6"},
	{ID: "claude-opus-4-6"},
}

// lookupModel returns the catalog entry for id, if any.
func lookupModel(id string) (modelOption, bool) {
	for _, m := range modelCatalog {
		if m.ID == id {
			return m, true
		}
	}
	return modelOption{}, false
}

// modelIDs returns the catalog model IDs for the /api/config dropdown.
func modelIDs() []string {
	ids := make([]string, len(modelCatalog))
	for i, m := range modelCatalog {
		ids[i] = m.ID
	}
	return ids
}

// resolveRouting returns the effective model, base URL, and API-key env var
// for a plan. Precedence: plan frontmatter → model catalog → config defaults.
// The catalog beats config defaults because a model-specific endpoint (e.g.
// Kimi) must not be overridden by a global DEFAULT_BASE_URL meant for another
// provider.
func resolveRouting(plan *Plan, cfg *DispatchConfig) (model, baseURL, apiKeyEnv string) {
	model = plan.Model
	if model == "" {
		model = cfg.DefaultModel
	}

	baseURL = plan.BaseURL
	apiKeyEnv = plan.APIKeyEnv
	if m, ok := lookupModel(model); ok {
		if baseURL == "" {
			baseURL = m.BaseURL
		}
		if apiKeyEnv == "" {
			apiKeyEnv = m.APIKeyEnv
		}
	}
	if baseURL == "" {
		baseURL = cfg.DefaultBaseURL
	}
	if apiKeyEnv == "" {
		apiKeyEnv = cfg.DefaultAuthTokenEnv
	}
	if apiKeyEnv == "" {
		apiKeyEnv = "ANTHROPIC_API_KEY"
	}
	return model, baseURL, apiKeyEnv
}
