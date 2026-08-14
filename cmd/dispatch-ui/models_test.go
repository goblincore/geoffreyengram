package main

import "testing"

func TestResolveRouting(t *testing.T) {
	cfg := &DispatchConfig{
		DefaultModel:        "glm-5.1",
		DefaultBaseURL:      "https://default.example/api",
		DefaultAuthTokenEnv: "DEFAULT_KEY",
	}

	tests := []struct {
		name          string
		plan          *Plan
		cfg           *DispatchConfig
		wantModel     string
		wantBaseURL   string
		wantAPIKeyEnv string
	}{
		{
			name:          "glm-5.3 routes to z.ai over config defaults",
			plan:          &Plan{Model: "glm-5.3"},
			cfg:           cfg,
			wantModel:     "glm-5.3",
			wantBaseURL:   "https://api.z.ai/api/anthropic",
			wantAPIKeyEnv: "ZAI_API_KEY",
		},
		{
			name:          "kimi k3 routes to kimi endpoint and key",
			plan:          &Plan{Model: "k3"},
			cfg:           cfg,
			wantModel:     "k3",
			wantBaseURL:   "https://api.kimi.com/coding",
			wantAPIKeyEnv: "KIMI_API_KEY",
		},
		{
			name:          "plan frontmatter beats catalog",
			plan:          &Plan{Model: "k3", BaseURL: "https://proxy.example", APIKeyEnv: "MY_KEY"},
			cfg:           cfg,
			wantModel:     "k3",
			wantBaseURL:   "https://proxy.example",
			wantAPIKeyEnv: "MY_KEY",
		},
		{
			name:          "anthropic model falls through to config defaults",
			plan:          &Plan{Model: "claude-opus-4-6"},
			cfg:           cfg,
			wantModel:     "claude-opus-4-6",
			wantBaseURL:   "https://default.example/api",
			wantAPIKeyEnv: "DEFAULT_KEY",
		},
		{
			name:          "unknown model uses config defaults",
			plan:          &Plan{Model: "zai/glm-5.2:xhigh"},
			cfg:           cfg,
			wantModel:     "zai/glm-5.2:xhigh",
			wantBaseURL:   "https://default.example/api",
			wantAPIKeyEnv: "DEFAULT_KEY",
		},
		{
			name:          "empty model uses config default model and its routing",
			plan:          &Plan{},
			cfg:           cfg,
			wantModel:     "glm-5.1",
			wantBaseURL:   "https://api.z.ai/api/anthropic",
			wantAPIKeyEnv: "ZAI_API_KEY",
		},
		{
			name:          "no defaults anywhere falls back to ANTHROPIC_API_KEY",
			plan:          &Plan{Model: "claude-opus-4-6"},
			cfg:           &DispatchConfig{},
			wantModel:     "claude-opus-4-6",
			wantBaseURL:   "",
			wantAPIKeyEnv: "ANTHROPIC_API_KEY",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model, baseURL, apiKeyEnv := resolveRouting(tt.plan, tt.cfg)
			if model != tt.wantModel {
				t.Errorf("model = %q, want %q", model, tt.wantModel)
			}
			if baseURL != tt.wantBaseURL {
				t.Errorf("baseURL = %q, want %q", baseURL, tt.wantBaseURL)
			}
			if apiKeyEnv != tt.wantAPIKeyEnv {
				t.Errorf("apiKeyEnv = %q, want %q", apiKeyEnv, tt.wantAPIKeyEnv)
			}
		})
	}
}

func TestModelIDsIncludeNewModels(t *testing.T) {
	ids := modelIDs()
	want := map[string]bool{"glm-5.3": false, "k3": false}
	for _, id := range ids {
		if _, ok := want[id]; ok {
			want[id] = true
		}
	}
	for id, found := range want {
		if !found {
			t.Errorf("modelIDs() missing %q", id)
		}
	}
}

func TestPiModelCatalogIncludesNewModels(t *testing.T) {
	want := map[string]bool{"zai/glm-5.3": false, "kimi/k3": false}
	for _, ref := range piModelCatalog {
		if _, ok := want[ref]; ok {
			want[ref] = true
		}
	}
	for ref, found := range want {
		if !found {
			t.Errorf("piModelCatalog missing %q", ref)
		}
	}
}
