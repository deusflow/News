package app

import (
	"fmt"
	"os"
	"strings"

	"github.com/deusflow/News/internal/ai"
	"github.com/deusflow/News/internal/ai/gemini"
	"github.com/deusflow/News/internal/ai/groq"
	"github.com/deusflow/News/internal/config"
	"github.com/deusflow/News/internal/logger"
)

// initAIProviders parses configured provider names (e.g. "gemini", "groq:llama-3.3-70b-versatile",
// "openrouter:meta-llama/llama-3.3-70b-instruct:free") and instantiates the client fallback chain.
func initAIProviders(cfg *config.Config) []ai.Provider {
	var aiProviders []ai.Provider

	for _, pName := range cfg.AI.Providers {
		pLower := strings.TrimSpace(strings.ToLower(pName))
		base, customModel, hasModel := strings.Cut(pLower, ":")
		switch base {
		case "gemini":
			keys := []string{cfg.AI.GeminiAPIKey, cfg.AI.GeminiAPIKey2, cfg.AI.GeminiAPIKey3}
			model := cfg.AI.GeminiModel
			if hasModel && customModel != "" {
				model = customModel
			}
			client, err := gemini.NewClient(keys, model)
			if err != nil {
				logger.Error("Failed to init Gemini", "error", err)
				continue
			}
			aiProviders = append(aiProviders, client)
			logger.Info("AI Provider added", "name", "gemini", "model", model)

		case "gemini-flash-lite":
			keys := []string{cfg.AI.GeminiAPIKey, cfg.AI.GeminiAPIKey2, cfg.AI.GeminiAPIKey3}
			model := os.Getenv("GEMINI_FLASH_LITE_MODEL")
			if hasModel && customModel != "" {
				model = customModel
			}
			if model == "" {
				logger.Warn("Gemini Flash Lite model is not specified, skipping (set GEMINI_FLASH_LITE_MODEL or use gemini-flash-lite:<model>)")
				continue
			}
			client, err := gemini.NewClient(keys, model)
			if err != nil {
				logger.Error("Failed to init Gemini Flash Lite", "error", err)
				continue
			}
			aiProviders = append(aiProviders, client)
			logger.Info("AI Provider added", "name", "gemini-flash-lite", "model", model)

		case "groq":
			groqKeys := []string{cfg.AI.GroqAPIKey, cfg.AI.GroqAPIKey2, cfg.AI.GroqAPIKey3}
			var validGroqKeys []string
			for _, k := range groqKeys {
				if strings.TrimSpace(k) != "" {
					validGroqKeys = append(validGroqKeys, strings.TrimSpace(k))
				}
			}
			if len(validGroqKeys) > 0 {
				model := cfg.AI.GroqModel
				if hasModel && customModel != "" {
					model = customModel
				}
				name := "groq"
				if hasModel && customModel != "" {
					name = fmt.Sprintf("groq:%s", customModel)
				}
				baseURL := os.Getenv("GROQ_BASE_URL")
				if baseURL == "" {
					baseURL = "https://api.groq.com/openai/v1/chat/completions"
				}
				client := groq.NewClientWithKeys(name, validGroqKeys, model, baseURL)
				aiProviders = append(aiProviders, client)
				logger.Info("AI Provider added", "name", name, "model", model, "keys_count", len(validGroqKeys))
			} else {
				logger.Warn("Groq API Key is missing, skipping")
			}

		case "cerebras":
			key := os.Getenv("CEREBRAS_API_KEY")
			if key != "" {
				model := os.Getenv("CEREBRAS_MODEL")
				if hasModel && customModel != "" {
					model = customModel
				}
				if model == "" {
					logger.Warn("Cerebras model is not specified, skipping (set CEREBRAS_MODEL or use cerebras:<model>)")
					continue
				}
				name := "cerebras"
				if hasModel && customModel != "" {
					name = fmt.Sprintf("cerebras:%s", customModel)
				}
				baseURL := os.Getenv("CEREBRAS_BASE_URL")
				if baseURL == "" {
					baseURL = "https://api.cerebras.ai/v1/chat/completions"
				}
				client := groq.NewClientWithOptions(name, key, model, baseURL)
				aiProviders = append(aiProviders, client)
				logger.Info("AI Provider added", "name", name, "model", model)
			} else {
				logger.Warn("Cerebras API Key is missing, skipping")
			}

		case "openrouter":
			var openRouterKeys []string
			if k := strings.TrimSpace(cfg.AI.OpenRouterAPIKey2); k != "" {
				openRouterKeys = append(openRouterKeys, k)
			}
			if k := strings.TrimSpace(cfg.AI.OpenRouterAPIKey); k != "" {
				openRouterKeys = append(openRouterKeys, k)
			}
			if len(openRouterKeys) == 0 {
				if k := strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY_2")); k != "" {
					openRouterKeys = append(openRouterKeys, k)
				}
				if k := strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY")); k != "" {
					openRouterKeys = append(openRouterKeys, k)
				}
			}

			if len(openRouterKeys) > 0 {
				model := cfg.AI.OpenRouterModel
				if model == "" {
					model = os.Getenv("OPENROUTER_MODEL")
				}
				if hasModel && customModel != "" {
					model = customModel
				}
				if model == "" {
					model = "meta-llama/llama-3.3-70b-instruct:free"
				}
				name := "openrouter"
				if hasModel && customModel != "" {
					name = fmt.Sprintf("openrouter:%s", customModel)
				}
				baseURL := os.Getenv("OPENROUTER_BASE_URL")
				if baseURL == "" {
					baseURL = "https://openrouter.ai/api/v1/chat/completions"
				}
				client := groq.NewClientWithKeys(name, openRouterKeys, model, baseURL)
				aiProviders = append(aiProviders, client)
				logger.Info("AI Provider added", "name", name, "model", model, "keys_count", len(openRouterKeys))
			} else {
				logger.Warn("OpenRouter API Key (OPENROUTER_API_KEY_2 or OPENROUTER_API_KEY) is missing, skipping")
			}

		case "openai":
			key := os.Getenv("OPENAI_API_KEY")
			if key != "" {
				model := os.Getenv("OPENAI_MODEL")
				if hasModel && customModel != "" {
					model = customModel
				}
				if model == "" {
					logger.Warn("OpenAI model is not specified, skipping (set OPENAI_MODEL or use openai:<model>)")
					continue
				}
				name := "openai"
				if hasModel && customModel != "" {
					name = fmt.Sprintf("openai:%s", customModel)
				}
				baseURL := os.Getenv("OPENAI_BASE_URL")
				if baseURL == "" {
					baseURL = "https://api.openai.com/v1/chat/completions"
				}
				client := groq.NewClientWithOptions(name, key, model, baseURL)
				aiProviders = append(aiProviders, client)
				logger.Info("AI Provider added", "name", name, "model", model)
			} else {
				logger.Warn("OpenAI API Key is missing, skipping")
			}

		case "qwen":
			key := os.Getenv("QWEN_API_KEY")
			if key == "" {
				key = os.Getenv("DASHSCOPE_API_KEY")
			}
			if key != "" {
				model := os.Getenv("QWEN_MODEL")
				if hasModel && customModel != "" {
					model = customModel
				}
				if model == "" {
					logger.Warn("Qwen model is not specified, skipping (set QWEN_MODEL or use qwen:<model>)")
					continue
				}
				name := "qwen"
				if hasModel && customModel != "" {
					name = fmt.Sprintf("qwen:%s", customModel)
				}
				baseURL := os.Getenv("QWEN_BASE_URL")
				if baseURL == "" {
					baseURL = "https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions"
				}
				client := groq.NewClientWithOptions(name, key, model, baseURL)
				aiProviders = append(aiProviders, client)
				logger.Info("AI Provider added", "name", name, "model", model)
			} else if cfg.AI.GroqAPIKey != "" {
				model := os.Getenv("QWEN_MODEL")
				if hasModel && customModel != "" {
					model = customModel
				}
				if model == "" {
					logger.Warn("Qwen model for Groq is not specified, skipping (set QWEN_MODEL or use qwen:<model>)")
					continue
				}
				name := "qwen (via Groq)"
				if hasModel && customModel != "" {
					name = fmt.Sprintf("qwen:%s (via Groq)", customModel)
				}
				baseURL := os.Getenv("GROQ_BASE_URL")
				if baseURL == "" {
					baseURL = "https://api.groq.com/openai/v1/chat/completions"
				}
				client := groq.NewClientWithOptions(name, cfg.AI.GroqAPIKey, model, baseURL)
				aiProviders = append(aiProviders, client)
				logger.Info("AI Provider added", "name", name, "model", model)
			} else {
				logger.Warn("Neither Qwen/DashScope nor Groq API Key available for Qwen, skipping")
			}

		default:
			logger.Warn("Unknown AI provider in config, skipping", "provider", pName)
		}
	}

	return aiProviders
}

