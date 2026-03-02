package pricing

import (
	"regexp"
	"strings"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/logs"
)

const tokensPerMillion = 1_000_000.0

var dateSuffixPattern = regexp.MustCompile(`-(\d{8}|\d{4}-\d{2}-\d{2}|\d{4})`)

// TokenPrice represents per-1M token pricing for a model.
type TokenPrice struct {
	InputPer1M       float64
	OutputPer1M      float64
	CacheReadPer1M   float64
	CacheCreatePer1M float64
}

// NormalizeModelName standardizes model names to improve price lookups.
func NormalizeModelName(model string) string {
	name := strings.TrimSpace(strings.ToLower(model))
	if name == "" {
		return ""
	}

	name = strings.ReplaceAll(name, "_", "-")
	name = strings.ReplaceAll(name, " ", "-")
	name = dateSuffixPattern.ReplaceAllString(name, "")

	// Normalize common Claude variations.
	if strings.HasPrefix(name, "claude-3-5-") {
		name = strings.Replace(name, "claude-3-5-", "claude-3.5-", 1)
	}
	// Normalize common OpenAI variations.
	if strings.HasPrefix(name, "gpt4o") {
		name = strings.Replace(name, "gpt4o", "gpt-4o", 1)
	}

	return name
}

// PriceFor returns the token price for a provider+model combination.
func PriceFor(provider, model string) (TokenPrice, bool) {
	provider = normalizeProvider(provider)
	if provider == "" {
		return TokenPrice{}, false
	}

	normalized := normalizeModelForProvider(provider, model)

	switch provider {
	case "claude":
		price, ok := ClaudePricing[normalized]
		return price, ok
	case "codex", "openai":
		price, ok := OpenAIPricing[normalized]
		return price, ok
	case "gemini":
		price, ok := GeminiPricing[normalized]
		return price, ok
	case "opencode", "cursor":
		// Pricing not yet available for these providers
		return TokenPrice{}, false
	default:
		return TokenPrice{}, false
	}
}

// CalculateCost computes the total cost for token usage.
// If model is empty, it will sum costs for all known models in usage.ByModel.
func CalculateCost(usage *logs.TokenUsage, model, provider string) float64 {
	if usage == nil {
		return 0
	}

	provider = normalizeProvider(provider)
	if provider == "" {
		return 0
	}

	if model == "" {
		return calculateCostAllModels(usage, provider)
	}

	price, ok := PriceFor(provider, model)
	if !ok {
		return 0
	}

	if len(usage.ByModel) > 0 {
		mu := findModelUsage(usage, provider, model)
		if mu == nil {
			return 0
		}
		cacheRead := int64(0)
		cacheCreate := int64(0)
		if len(usage.ByModel) == 1 {
			cacheRead = usage.CacheReadTokens
			cacheCreate = usage.CacheCreateTokens
		}
		return costFromTokens(mu.InputTokens, mu.OutputTokens, cacheRead, cacheCreate, price)
	}

	return costFromTokens(
		usage.InputTokens,
		usage.OutputTokens,
		usage.CacheReadTokens,
		usage.CacheCreateTokens,
		price,
	)
}

func calculateCostAllModels(usage *logs.TokenUsage, provider string) float64 {
	if usage == nil || len(usage.ByModel) == 0 {
		return 0
	}

	total := 0.0
	var singlePrice TokenPrice
	var singlePriceOK bool

	for modelName, mu := range usage.ByModel {
		price, ok := PriceFor(provider, modelName)
		if !ok {
			continue
		}
		total += costFromTokens(mu.InputTokens, mu.OutputTokens, 0, 0, price)
		if len(usage.ByModel) == 1 {
			singlePrice = price
			singlePriceOK = true
		}
	}

	if len(usage.ByModel) == 1 && singlePriceOK {
		total += costFromTokens(0, 0, usage.CacheReadTokens, usage.CacheCreateTokens, singlePrice)
	}

	return total
}

func costFromTokens(input, output, cacheRead, cacheCreate int64, price TokenPrice) float64 {
	input = nonNegativeTokens(input)
	output = nonNegativeTokens(output)
	cacheRead = nonNegativeTokens(cacheRead)
	cacheCreate = nonNegativeTokens(cacheCreate)

	return (float64(input)/tokensPerMillion)*price.InputPer1M +
		(float64(output)/tokensPerMillion)*price.OutputPer1M +
		(float64(cacheRead)/tokensPerMillion)*price.CacheReadPer1M +
		(float64(cacheCreate)/tokensPerMillion)*price.CacheCreatePer1M
}

func nonNegativeTokens(tokens int64) int64 {
	if tokens < 0 {
		return 0
	}
	return tokens
}

func findModelUsage(usage *logs.TokenUsage, provider, model string) *logs.ModelTokenUsage {
	if usage == nil || len(usage.ByModel) == 0 {
		return nil
	}

	target := normalizeModelForProvider(provider, model)
	for name, mu := range usage.ByModel {
		if normalizeModelForProvider(provider, name) == target {
			return mu
		}
	}

	return nil
}

func normalizeProvider(provider string) string {
	return strings.TrimSpace(strings.ToLower(provider))
}

func normalizeModelForProvider(provider, model string) string {
	normalized := NormalizeModelName(model)
	switch provider {
	case "claude":
		if alias, ok := claudeAliases[normalized]; ok {
			return alias
		}
	case "codex", "openai":
		if alias, ok := openAIAliases[normalized]; ok {
			return alias
		}
	case "gemini":
		if alias, ok := geminiAliases[normalized]; ok {
			return alias
		}
	case "opencode", "cursor":
		// No model aliases for these providers yet
	}
	return normalized
}
