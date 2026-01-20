package utils

import (
	"regexp"
	"strings"
)

// BrandPrefixRule defines a rule for matching model names to brand prefixes
type BrandPrefixRule struct {
	Patterns      []*regexp.Regexp
	LowercaseSlug string // lowercase prefix slug (e.g., "openai/")
	OfficialName  string // official brand name with proper capitalization (e.g., "OpenAI/")
}

// compilePatterns compiles multiple regex patterns with case-insensitive flag
func compilePatterns(patterns ...string) []*regexp.Regexp {
	result := make([]*regexp.Regexp, 0, len(patterns))
	for _, p := range patterns {
		re := regexp.MustCompile("(?i)" + p)
		result = append(result, re)
	}
	return result
}

// brandPrefixRules contains all brand prefix matching rules
// Patterns are pre-compiled and matched case-insensitively against model names
// Rules are ordered by priority - more specific patterns should come first
var brandPrefixRules = []BrandPrefixRule{
	// DeepSeek models (high priority - check before others)
	{Patterns: compilePatterns(`deepseek`), LowercaseSlug: "deepseek/", OfficialName: "DeepSeek/"},

	// GPT-SoVITS voice cloning models (must be before OpenAI to avoid gpt pattern conflict)
	{Patterns: compilePatterns(`gpt-sovits`, `sovits`), LowercaseSlug: "sovits/", OfficialName: "SoVITS/"},

	// Groq hosted models (must be before Mistral/Meta to catch groq-prefixed models)
	{Patterns: compilePatterns(`^groq-`), LowercaseSlug: "groq/", OfficialName: "Groq/"},

	// Together AI hosted models (must be before Meta to catch together-prefixed models)
	{Patterns: compilePatterns(`^together-`), LowercaseSlug: "together/", OfficialName: "Together/"},

	// BAAI BGE embedding and reranking models (must be before Google to catch bge-*-gemma models)
	{Patterns: compilePatterns(`\bbge-`, `\bbge\d`), LowercaseSlug: "baai/", OfficialName: "BAAI/"},

	// OpenAI GPT and o-series models
	// Note: text-embedding-ada, text-embedding-3 are OpenAI; text-embedding-004 is Google
	// Note: tts-1 pattern is specific to avoid matching qwen-tts
	{Patterns: compilePatterns(`\bgpt\b`, `^o[1-4]$`, `^o[1-4]-`, `chatgpt`, `text-davinci`, `text-embedding-ada`, `text-embedding-3`, `whisper`, `dall-e`, `\btts-1`), LowercaseSlug: "openai/", OfficialName: "OpenAI/"},

	// Google Gemini/Gemma/Embedding models
	{Patterns: compilePatterns(`gemini`, `gemma`, `palm`, `bard`, `embedding-gecko`, `^embedding-\d`, `^text-embedding-\d`), LowercaseSlug: "google/", OfficialName: "Google/"},

	// Anthropic Claude models
	{Patterns: compilePatterns(`claude`), LowercaseSlug: "anthropic/", OfficialName: "Anthropic/"},

	// GLM/ChatGLM models (Z.ai / Zhipu AI / THUDM - Tsinghua University)
	{Patterns: compilePatterns(`\bglm\b`, `\bglm\d`, `chatglm`, `zhipu`, `cogview`, `cogvideo`, `thudm`, `\bz-ai\b`, `\bzai\b`), LowercaseSlug: "glm/", OfficialName: "GLM/"},

	// Kimi/Moonshot models (Moonshot AI)
	{Patterns: compilePatterns(`\bkimi\b`, `moonshot`), LowercaseSlug: "kimi/", OfficialName: "Kimi/"},

	// Nous Research models (must be before llama to avoid conflict with hermes-llama)
	// Note: hermes pattern matches both "hermes" and "deephermes"
	{Patterns: compilePatterns(`\bnous\b`, `hermes`), LowercaseSlug: "nous/", OfficialName: "Nous/"},

	// DeepCogito models (must be before llama to catch cogito-*-llama models)
	{Patterns: compilePatterns(`deepcogito`, `\bcogito\b`), LowercaseSlug: "deepcogito/", OfficialName: "DeepCogito/"},

	// NeverSleep models (must be before llama to catch lumimaid/noromaid models)
	{Patterns: compilePatterns(`neversleep`, `\bnoromaid\b`, `\blumimaid\b`), LowercaseSlug: "neversleep/", OfficialName: "NeverSleep/"},

	// Cognitive Computations models (must be before mistral to catch dolphin-mistral models)
	{Patterns: compilePatterns(`cognitivecomputations`, `\bdolphin\b`), LowercaseSlug: "cognitivecomputations/", OfficialName: "CognitiveComputations/"},

	// Qwen/Tongyi models (Alibaba Cloud) - includes Qwen LLM, QVQ, Wanx image models, FunAudioLLM/SenseVoice, DeepResearch
	// Note: qwen-tts must be matched here before OpenAI's tts- pattern
	{Patterns: compilePatterns(`qwen`, `tongyi`, `qwq`, `\bqvq\b`, `wanx`, `wan2`, `sensevoice`, `funaudio`, `deepresearch`), LowercaseSlug: "tongyi/", OfficialName: "Tongyi/"},

	// Meta Llama models (includes llama, llama2, llama3, llama3.1, llama3.3, llama4, etc.)
	{Patterns: compilePatterns(`\bllama\b`, `llama\d`, `codellama`), LowercaseSlug: "meta/", OfficialName: "Meta/"},

	// Mistral models
	{Patterns: compilePatterns(`mistral`, `mixtral`, `codestral`, `pixtral`, `magistral`, `devstral`, `ministral`, `voxtral`), LowercaseSlug: "mistral/", OfficialName: "Mistral/"},

	// Cohere models
	{Patterns: compilePatterns(`\bcommand\b`, `cohere`, `\bc4ai\b`, `\baya\b`), LowercaseSlug: "cohere/", OfficialName: "Cohere/"},

	// xAI Grok models
	{Patterns: compilePatterns(`\bgrok\b`), LowercaseSlug: "xai/", OfficialName: "xAI/"},

	// Amazon Nova/Titan models
	{Patterns: compilePatterns(`\bnova-`, `\btitan-`, `amazon-titan`), LowercaseSlug: "amazon/", OfficialName: "Amazon/"},

	// Microsoft Phi models
	{Patterns: compilePatterns(`\bphi-`), LowercaseSlug: "microsoft/", OfficialName: "Microsoft/"},

	// Yi models (01.AI)
	{Patterns: compilePatterns(`\byi-`, `\byi\d`), LowercaseSlug: "yi/", OfficialName: "Yi/"},

	// Baichuan models
	{Patterns: compilePatterns(`baichuan`), LowercaseSlug: "baichuan/", OfficialName: "Baichuan/"},

	// MiniMax models
	{Patterns: compilePatterns(`minimax`, `\babab`), LowercaseSlug: "minimax/", OfficialName: "MiniMax/"},

	// Doubao/ByteDance models (Doubao is ByteDance's consumer brand)
	{Patterns: compilePatterns(`doubao`, `skylark`), LowercaseSlug: "doubao/", OfficialName: "Doubao/"},

	// ByteDance Seed models (research/open-source models)
	{Patterns: compilePatterns(`\bseed-oss\b`, `\bseed-\d`), LowercaseSlug: "bytedance/", OfficialName: "ByteDance/"},

	// Hunyuan/Tencent models (Hunyuan is Tencent's AI brand)
	{Patterns: compilePatterns(`hunyuan`, `tencent`), LowercaseSlug: "tencent/", OfficialName: "Tencent/"},

	// Spark/iFlytek models
	{Patterns: compilePatterns(`\bspark\b`), LowercaseSlug: "spark/", OfficialName: "Spark/"},

	// ERNIE/Baidu models
	{Patterns: compilePatterns(`ernie`, `wenxin`), LowercaseSlug: "baidu/", OfficialName: "Baidu/"},

	// Stability AI models (sd3, sdxl, stable-diffusion)
	{Patterns: compilePatterns(`stable-diffusion`, `\bsdxl\b`, `\bsd3\b`, `stability-ai`), LowercaseSlug: "stability/", OfficialName: "Stability/"},

	// Perplexity models
	{Patterns: compilePatterns(`sonar`, `\bpplx\b`, `perplexity`), LowercaseSlug: "perplexity/", OfficialName: "Perplexity/"},

	// TII Falcon models
	{Patterns: compilePatterns(`\bfalcon\b`), LowercaseSlug: "tii/", OfficialName: "TII/"},

	// InternLM/InternVL models (Shanghai AI Lab / OpenGVLab)
	{Patterns: compilePatterns(`internlm`, `internvl`, `opengvlab`), LowercaseSlug: "internlm/", OfficialName: "InternLM/"},

	// WizardLM models
	{Patterns: compilePatterns(`wizardlm`, `wizardcoder`, `wizardmath`), LowercaseSlug: "wizardlm/", OfficialName: "WizardLM/"},

	// LMSYS models (Vicuna)
	{Patterns: compilePatterns(`vicuna`), LowercaseSlug: "lmsys/", OfficialName: "LMSYS/"},

	// OpenChat models
	{Patterns: compilePatterns(`openchat`), LowercaseSlug: "openchat/", OfficialName: "OpenChat/"},

	// HuggingFace models (Zephyr)
	{Patterns: compilePatterns(`zephyr`), LowercaseSlug: "huggingface/", OfficialName: "HuggingFace/"},

	// BigCode models (StarCoder)
	{Patterns: compilePatterns(`starcoder`, `starchat`), LowercaseSlug: "bigcode/", OfficialName: "BigCode/"},

	// Salesforce models (CodeGen)
	{Patterns: compilePatterns(`codegen`), LowercaseSlug: "salesforce/", OfficialName: "Salesforce/"},

	// NVIDIA models (Nemotron, embed-qa)
	{Patterns: compilePatterns(`nemotron`, `nvidia`, `\bembed-qa\b`), LowercaseSlug: "nvidia/", OfficialName: "NVIDIA/"},

	// IBM Granite models
	{Patterns: compilePatterns(`\bgranite\b`, `ibm-granite`), LowercaseSlug: "ibm/", OfficialName: "IBM/"},

	// Essential AI models (RNJ)
	{Patterns: compilePatterns(`\brnj-`, `essentialai`), LowercaseSlug: "essentialai/", OfficialName: "EssentialAI/"},

	// Arcee AI models (Trinity, Maestro, Virtuoso, Spotlight, Coder)
	{Patterns: compilePatterns(`arcee-ai`, `\btrinity\b`, `\bmaestro\b`, `\bvirtuoso\b`, `\bspotlight\b`), LowercaseSlug: "arcee/", OfficialName: "Arcee/"},

	// Aion Labs models (drug discovery AI)
	{Patterns: compilePatterns(`aion-labs`, `\baion-`), LowercaseSlug: "aion-labs/", OfficialName: "AionLabs/"},

	// Liquid AI models (LFM)
	{Patterns: compilePatterns(`\blfm\b`, `lfm-`, `lfm2`, `liquid`), LowercaseSlug: "liquid/", OfficialName: "Liquid/"},

	// Inception Labs models (Mercury)
	{Patterns: compilePatterns(`\bmercury\b`, `inception`), LowercaseSlug: "inception/", OfficialName: "Inception/"},

	// Morph AI models
	{Patterns: compilePatterns(`\bmorph\b`, `morph-`), LowercaseSlug: "morph/", OfficialName: "Morph/"},

	// Inflection AI models
	{Patterns: compilePatterns(`inflection-`, `inflection\d`), LowercaseSlug: "inflection/", OfficialName: "Inflection/"},

	// Prime Intellect models (Intellect)
	{Patterns: compilePatterns(`prime-intellect`, `\bintellect-`), LowercaseSlug: "prime-intellect/", OfficialName: "PrimeIntellect/"},

	// TNG Technology Consulting models (Chimera)
	{Patterns: compilePatterns(`tngtech`, `\bchimera\b`), LowercaseSlug: "tng/", OfficialName: "TNG/"},

	// Anthracite-org models (Magnum)
	{Patterns: compilePatterns(`anthracite`, `\bmagnum\b`), LowercaseSlug: "anthracite/", OfficialName: "Anthracite/"},

	// Relace AI models
	{Patterns: compilePatterns(`relace-`), LowercaseSlug: "relace/", OfficialName: "Relace/"},

	// Nex-AGI models
	{Patterns: compilePatterns(`nex-agi`, `\bnex-n\d`), LowercaseSlug: "nex-agi/", OfficialName: "NexAGI/"},

	// Allen AI models (OLMo, Molmo)
	{Patterns: compilePatterns(`\bolmo\b`, `\bmolmo\b`, `allenai`), LowercaseSlug: "allenai/", OfficialName: "AllenAI/"},

	// EleutherAI models (Llemma - math LLM)
	// Note: llemma pattern without word boundary to match llemma_7b
	{Patterns: compilePatterns(`eleutherai`, `llemma`), LowercaseSlug: "eleutherai/", OfficialName: "EleutherAI/"},

	// Mancer models (Weaver)
	{Patterns: compilePatterns(`\bmancer\b`, `\bweaver\b`), LowercaseSlug: "mancer/", OfficialName: "Mancer/"},

	// Gryphe models (MythoMax)
	{Patterns: compilePatterns(`gryphe`, `\bmythomax\b`), LowercaseSlug: "gryphe/", OfficialName: "Gryphe/"},

	// Undi95 models (ReMM)
	{Patterns: compilePatterns(`undi95`, `\bremm\b`), LowercaseSlug: "undi95/", OfficialName: "Undi95/"},

	// Sao10k models (Euryale, Lunaris, Hanami)
	{Patterns: compilePatterns(`sao10k`, `\beuryale\b`, `\blunaris\b`, `\bhanami\b`), LowercaseSlug: "sao10k/", OfficialName: "Sao10k/"},

	// TheDrummer models (Cydonia, Rocinante, Skyfall, Unslopnemo)
	{Patterns: compilePatterns(`thedrummer`, `\bcydonia\b`, `\brocinante\b`, `\bskyfall\b`, `\bunslopnemo\b`), LowercaseSlug: "thedrummer/", OfficialName: "TheDrummer/"},

	// Alpindale models (Goliath)
	{Patterns: compilePatterns(`alpindale`, `\bgoliath\b`), LowercaseSlug: "alpindale/", OfficialName: "Alpindale/"},

	// Raifle models (SorcererLM)
	{Patterns: compilePatterns(`raifle`, `\bsorcererlm\b`), LowercaseSlug: "raifle/", OfficialName: "Raifle/"},

	// SwitchPoint models (Router)
	{Patterns: compilePatterns(`switchpoint`), LowercaseSlug: "switchpoint/", OfficialName: "SwitchPoint/"},

	// AI21 models (Jamba, Jurassic)
	{Patterns: compilePatterns(`jamba`, `jurassic`, `\bai21\b`), LowercaseSlug: "ai21/", OfficialName: "AI21/"},

	// Databricks models (DBRX)
	{Patterns: compilePatterns(`dbrx`), LowercaseSlug: "databricks/", OfficialName: "Databricks/"},

	// Snowflake models (Arctic)
	{Patterns: compilePatterns(`arctic`, `snowflake`), LowercaseSlug: "snowflake/", OfficialName: "Snowflake/"},

	// Reka models
	{Patterns: compilePatterns(`\breka\b`), LowercaseSlug: "reka/", OfficialName: "Reka/"},

	// ==================== Embedding & Reranking Models ====================

	// Jina AI embedding, reranking and CLIP models
	{Patterns: compilePatterns(`jina-embeddings`, `jina-reranker`, `jina-clip`, `jina-colbert`, `jina-vlm`), LowercaseSlug: "jina/", OfficialName: "Jina/"},

	// Voyage AI embedding models
	{Patterns: compilePatterns(`voyage-`), LowercaseSlug: "voyage/", OfficialName: "Voyage/"},

	// Nomic AI embedding models
	{Patterns: compilePatterns(`nomic-embed`, `nomic-ai`), LowercaseSlug: "nomic/", OfficialName: "Nomic/"},

	// Mixedbread AI reranking models
	{Patterns: compilePatterns(`mxbai-`), LowercaseSlug: "mixedbread/", OfficialName: "Mixedbread/"},

	// NetEase Youdao BCE embedding and reranking models
	{Patterns: compilePatterns(`\bbce-embedding`, `\bbce-reranker`), LowercaseSlug: "youdao/", OfficialName: "Youdao/"},

	// Alibaba GTE embedding models
	{Patterns: compilePatterns(`\bgte-`), LowercaseSlug: "alibaba/", OfficialName: "Alibaba/"},

	// ==================== Video Generation Models ====================

	// OpenAI Sora video models
	{Patterns: compilePatterns(`\bsora\b`), LowercaseSlug: "openai/", OfficialName: "OpenAI/"},

	// Google Veo video models
	{Patterns: compilePatterns(`\bveo\b`, `veo-`), LowercaseSlug: "google/", OfficialName: "Google/"},

	// Runway video models (Gen-2, Gen-3, Gen-4)
	{Patterns: compilePatterns(`runway`, `\bgen-[234]\b`, `gen-[234]-`), LowercaseSlug: "runway/", OfficialName: "Runway/"},

	// Pika Labs video models
	{Patterns: compilePatterns(`\bpika\b`, `pika-`), LowercaseSlug: "pika/", OfficialName: "Pika/"},

	// Kuaishou Kling video models, Kolors image models, and Kwaipilot code models
	{Patterns: compilePatterns(`\bkling\b`, `kling-`, `\bkolors\b`, `kwaipilot`, `\bkat-dev\b`, `\bkat-v\d`), LowercaseSlug: "kuaishou/", OfficialName: "Kuaishou/"},

	// Luma AI Dream Machine video models
	{Patterns: compilePatterns(`\bluma\b`, `dream-machine`, `\bray-?[23]\b`), LowercaseSlug: "luma/", OfficialName: "Luma/"},

	// MiniMax Hailuo video models
	{Patterns: compilePatterns(`hailuo`), LowercaseSlug: "minimax/", OfficialName: "MiniMax/"},

	// ByteDance Seedance video models
	{Patterns: compilePatterns(`seedance`), LowercaseSlug: "bytedance/", OfficialName: "ByteDance/"},

	// PixVerse video models
	{Patterns: compilePatterns(`pixverse`), LowercaseSlug: "pixverse/", OfficialName: "PixVerse/"},

	// ==================== Image Generation Models ====================

	// Black Forest Labs FLUX image models
	{Patterns: compilePatterns(`\bflux\b`, `flux-`), LowercaseSlug: "bfl/", OfficialName: "BFL/"},

	// Midjourney image models
	{Patterns: compilePatterns(`midjourney`, `\bmj-`), LowercaseSlug: "midjourney/", OfficialName: "Midjourney/"},

	// Ideogram image models
	{Patterns: compilePatterns(`ideogram`), LowercaseSlug: "ideogram/", OfficialName: "Ideogram/"},

	// Leonardo AI image models
	{Patterns: compilePatterns(`leonardo`), LowercaseSlug: "leonardo/", OfficialName: "Leonardo/"},

	// ==================== Audio/Speech Models ====================

	// ElevenLabs TTS models
	{Patterns: compilePatterns(`eleven_`, `elevenlabs`), LowercaseSlug: "elevenlabs/", OfficialName: "ElevenLabs/"},

	// Azure Speech models
	{Patterns: compilePatterns(`azure-tts`, `azure-speech`), LowercaseSlug: "azure/", OfficialName: "Azure/"},

	// Fish Audio models
	{Patterns: compilePatterns(`fish-speech`, `fish-audio`), LowercaseSlug: "fish/", OfficialName: "Fish/"},

	// ==================== Other Specialized Models ====================

	// Alibaba Wan video/image models
	{Patterns: compilePatterns(`\bwan-`, `\bwan\d`), LowercaseSlug: "alibaba/", OfficialName: "Alibaba/"},

	// SenseTime models
	{Patterns: compilePatterns(`sensetime`, `sensenova`), LowercaseSlug: "sensetime/", OfficialName: "SenseTime/"},

	// 360 AI models
	{Patterns: compilePatterns(`360gpt`, `360-ai`), LowercaseSlug: "360/", OfficialName: "360/"},

	// Zhipu models (non-GLM branded)
	{Patterns: compilePatterns(`zhipuai`), LowercaseSlug: "zhipu/", OfficialName: "Zhipu/"},

	// Xiaomi MiMo models
	{Patterns: compilePatterns(`\bmimo\b`, `mimo-`), LowercaseSlug: "xiaomi/", OfficialName: "Xiaomi/"},

	// Meituan LongCat models
	{Patterns: compilePatterns(`longcat`), LowercaseSlug: "meituan/", OfficialName: "Meituan/"},

	// Menlo/Jan models
	{Patterns: compilePatterns(`jan-nano`, `\bjan-`), LowercaseSlug: "menlo/", OfficialName: "Menlo/"},

	// StepFun models (阶跃星辰)
	{Patterns: compilePatterns(`\bstep-?[23]\b`, `step-?[23]-`, `stepfun`), LowercaseSlug: "stepfun/", OfficialName: "StepFun/"},

	// OpenCompass models (Shanghai AI Lab evaluation models)
	{Patterns: compilePatterns(`compassjudger`, `opencompass`), LowercaseSlug: "opencompass/", OfficialName: "OpenCompass/"},

	// Huawei Pangu models
	{Patterns: compilePatterns(`pangu`, `\bhuawei\b`, `ascend-tribe`), LowercaseSlug: "huawei/", OfficialName: "Huawei/"},

	// China Telecom TeleAI models
	{Patterns: compilePatterns(`teleai`, `telespeech`, `xingchen`), LowercaseSlug: "teleai/", OfficialName: "TeleAI/"},
}

// DetectBrandPrefix detects the brand prefix for a model name
// Returns the lowercase prefix slug (e.g., "openai/") or empty string if no match
// Detection is based on the model name itself, not the existing prefix
func DetectBrandPrefix(modelName string) string {
	if modelName == "" {
		return ""
	}

	// Strip any existing prefix before detection
	stripped := StripExistingPrefix(modelName)

	for _, rule := range brandPrefixRules {
		for _, pattern := range rule.Patterns {
			if pattern.MatchString(stripped) {
				return rule.LowercaseSlug
			}
		}
	}
	return ""
}

// DetectBrandPrefixWithOfficial detects the brand prefix and returns both lowercase and official name
// Returns (lowercaseSlug, officialName) or ("", "") if no match
// Detection is based on the model name itself, not the existing prefix
func DetectBrandPrefixWithOfficial(modelName string) (string, string) {
	if modelName == "" {
		return "", ""
	}

	// Strip any existing prefix before detection
	stripped := StripExistingPrefix(modelName)

	for _, rule := range brandPrefixRules {
		for _, pattern := range rule.Patterns {
			if pattern.MatchString(stripped) {
				return rule.LowercaseSlug, rule.OfficialName
			}
		}
	}
	return "", ""
}

// knownNonBrandPrefixes contains prefixes that are NOT brand names and should be stripped
// These are typically deployment/hosting prefixes like "lora/", "pro/", "openrouter/", etc.
var knownNonBrandPrefixes = []string{
	// Hosting/routing platforms (strip to reveal actual model)
	"openrouter/",  // OpenRouter hosting prefix
	"deepinfra/",   // DeepInfra hosting prefix
	"siliconflow/", // SiliconFlow hosting prefix
	"modelscope/",  // ModelScope hosting prefix
	"groq/",        // Groq hosting prefix
	"cerebras/",    // Cerebras hosting prefix
	"bailian/",     // Alibaba Bailian hosting prefix
	"geminicli/",   // GeminiCLI hosting prefix
	"highlight/",   // Highlight hosting prefix
	"warp/",        // Warp hosting prefix
	"akashchat/",   // AkashChat hosting prefix
	"bigmodel/",    // BigModel hosting prefix
	// Tier/quality prefixes
	"lora/",
	"pro/",
	"free/",
	"fast/",
	"turbo/",
	"premium/",
	"basic/",
	"standard/",
	"enterprise/",
}

// StripExistingPrefix removes any existing vendor/brand prefix from a model name
// A prefix is defined as text before the first "/" that is not at the start or end
// For multi-level prefixes like "lora/qwen/model", it strips known non-brand prefixes first
func StripExistingPrefix(modelName string) string {
	if modelName == "" {
		return ""
	}

	result := modelName

	// First, strip known non-brand prefixes (like lora/, pro/, etc.)
	// These can be nested, so we loop until no more are found
	for {
		stripped := false
		lowerResult := strings.ToLower(result)
		for _, prefix := range knownNonBrandPrefixes {
			if strings.HasPrefix(lowerResult, prefix) {
				result = result[len(prefix):]
				stripped = true
				break
			}
		}
		if !stripped {
			break
		}
	}

	// Now strip the first remaining prefix (which should be the brand prefix)
	idx := strings.Index(result, "/")

	// No slash, or slash at start, or slash at end - return as is
	if idx <= 0 || idx == len(result)-1 {
		return result
	}

	// Return everything after the first slash
	return result[idx+1:]
}

// glmNormalizationRegex matches GLM model names that need hyphen insertion (e.g., glm4.7 -> glm-4.7)
var glmNormalizationRegex = regexp.MustCompile(`(?i)^(glm)(\d)`)

// normalizeGLMModelName normalizes GLM model names by inserting hyphen between "glm" and version number
// e.g., "glm4.7" -> "glm-4.7", "GLM4.7" -> "GLM-4.7"
func normalizeGLMModelName(modelName string) string {
	return glmNormalizationRegex.ReplaceAllString(modelName, "${1}-${2}")
}

// ApplyBrandPrefix applies the appropriate brand prefix to a model name
// If useLowercase is true, uses lowercase prefix (e.g., "openai/")
// If useLowercase is false, uses official brand name (e.g., "OpenAI/")
// If no brand match and model has existing prefix, preserves the original prefix
// Returns the model name with prefix
func ApplyBrandPrefix(modelName string, useLowercase bool) string {
	if modelName == "" {
		return ""
	}

	// Strip any existing prefix
	stripped := StripExistingPrefix(modelName)

	// Normalize GLM model names (e.g., glm4.7 -> glm-4.7)
	stripped = normalizeGLMModelName(stripped)

	// Detect brand prefix
	lowercaseSlug, officialName := DetectBrandPrefixWithOfficial(stripped)

	// No match - preserve original prefix if exists, otherwise return stripped name
	if lowercaseSlug == "" {
		// Check if original model had a prefix (contains "/" not at start or end)
		idx := strings.Index(modelName, "/")
		if idx > 0 && idx < len(modelName)-1 {
			// Preserve original prefix
			return modelName
		}
		return stripped
	}

	// Apply appropriate prefix based on lowercase setting
	if useLowercase {
		return lowercaseSlug + stripped
	}
	return officialName + stripped
}

// ApplyBrandPrefixBatch applies brand prefixes to multiple model names
// Returns a map from original model name to prefixed model name
func ApplyBrandPrefixBatch(models []string, useLowercase bool) map[string]string {
	results := make(map[string]string, len(models))
	for _, model := range models {
		results[model] = ApplyBrandPrefix(model, useLowercase)
	}
	return results
}
