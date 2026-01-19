package utils

import (
	"testing"
)

func TestDetectBrandPrefix(t *testing.T) {
	tests := []struct {
		name       string
		modelName  string
		wantPrefix string
	}{
		// ==================== DeepSeek models ====================
		{"deepseek-chat", "deepseek-chat", "deepseek/"},
		{"deepseek-chat-search", "deepseek-chat-search", "deepseek/"},
		{"deepseek-reasoner", "deepseek-reasoner", "deepseek/"},
		{"deepseek-reasoner-search", "deepseek-reasoner-search", "deepseek/"},
		{"deepseek-v3", "deepseek-v3", "deepseek/"},
		{"deepseek-v3.1", "deepseek-v3.1", "deepseek/"},
		{"deepseek-v3.2", "deepseek-v3.2", "deepseek/"},
		{"DeepSeek-R1", "DeepSeek-R1", "deepseek/"},
		{"deepseek-r1-distill-qwen-32b", "deepseek-r1-distill-qwen-32b", "deepseek/"},
		{"deepseek-r1-distill-llama-70b", "deepseek-r1-distill-llama-70b", "deepseek/"},
		{"deepseek-coder", "deepseek-coder", "deepseek/"},
		{"deepseek-coder-v2", "deepseek-coder-v2", "deepseek/"},
		{"deepseek-prover-v2", "deepseek-prover-v2", "deepseek/"},

		// ==================== OpenAI GPT models ====================
		{"gpt-4", "gpt-4", "openai/"},
		{"gpt-4-turbo", "gpt-4-turbo", "openai/"},
		{"gpt-4-turbo-preview", "gpt-4-turbo-preview", "openai/"},
		{"gpt-4o", "gpt-4o", "openai/"},
		{"gpt-4o-mini", "gpt-4o-mini", "openai/"},
		{"gpt-4o-search-preview", "gpt-4o-search-preview", "openai/"},
		{"gpt-4.1", "gpt-4.1", "openai/"},
		{"gpt-4.1-mini", "gpt-4.1-mini", "openai/"},
		{"gpt-4.1-nano", "gpt-4.1-nano", "openai/"},
		{"gpt-4.5-preview", "gpt-4.5-preview", "openai/"},
		{"gpt-3.5-turbo", "gpt-3.5-turbo", "openai/"},
		{"gpt-3.5-turbo-16k", "gpt-3.5-turbo-16k", "openai/"},
		{"gpt-5", "gpt-5", "openai/"},
		{"gpt-5-mini", "gpt-5-mini", "openai/"},
		{"gpt-5-nano", "gpt-5-nano", "openai/"},
		{"gpt-5.1", "gpt-5.1", "openai/"},
		{"gpt-5.2", "gpt-5.2", "openai/"},
		{"gpt-5.2-pro", "gpt-5.2-pro", "openai/"},
		{"gpt-oss-120b", "gpt-oss-120b", "openai/"},
		{"gpt-image-1", "gpt-image-1", "openai/"},
		{"gpt-image-1.5", "gpt-image-1.5", "openai/"},
		{"gpt-audio", "gpt-audio", "openai/"},
		{"gpt-audio-mini", "gpt-audio-mini", "openai/"},
		{"gpt-realtime", "gpt-realtime", "openai/"},
		{"gpt-realtime-mini", "gpt-realtime-mini", "openai/"},

		// ==================== OpenAI o-series models ====================
		{"o1", "o1", "openai/"},
		{"o1-preview", "o1-preview", "openai/"},
		{"o1-mini", "o1-mini", "openai/"},
		{"o1-pro", "o1-pro", "openai/"},
		{"o3", "o3", "openai/"},
		{"o3-mini", "o3-mini", "openai/"},
		{"o3-pro", "o3-pro", "openai/"},
		{"o3-deep-research", "o3-deep-research", "openai/"},
		{"o4-mini", "o4-mini", "openai/"},
		{"o4-mini-high", "o4-mini-high", "openai/"},
		{"o4-mini-deep-research", "o4-mini-deep-research", "openai/"},
		{"chatgpt-4o-latest", "chatgpt-4o-latest", "openai/"},
		{"chatgpt-image-latest", "chatgpt-image-latest", "openai/"},
		{"text-davinci-003", "text-davinci-003", "openai/"},
		{"text-embedding-ada-002", "text-embedding-ada-002", "openai/"},
		{"text-embedding-3-small", "text-embedding-3-small", "openai/"},
		{"text-embedding-3-large", "text-embedding-3-large", "openai/"},
		{"whisper-1", "whisper-1", "openai/"},
		{"whisper-large-v3", "whisper-large-v3", "openai/"},
		{"dall-e-2", "dall-e-2", "openai/"},
		{"dall-e-3", "dall-e-3", "openai/"},
		{"tts-1", "tts-1", "openai/"},
		{"tts-1-hd", "tts-1-hd", "openai/"},

		// ==================== Google Gemini/Gemma models ====================
		{"gemini-pro", "gemini-pro", "google/"},
		{"gemini-1.0-pro", "gemini-1.0-pro", "google/"},
		{"gemini-1.5-pro", "gemini-1.5-pro", "google/"},
		{"gemini-1.5-flash", "gemini-1.5-flash", "google/"},
		{"gemini-2.0-flash", "gemini-2.0-flash", "google/"},
		{"gemini-2.0-flash-exp", "gemini-2.0-flash-exp", "google/"},
		{"gemini-2.5-pro", "gemini-2.5-pro", "google/"},
		{"gemini-3-pro", "gemini-3-pro", "google/"},
		{"gemini-3-pro-high", "gemini-3-pro-high", "google/"},
		{"gemma-7b", "gemma-7b", "google/"},
		{"gemma-2-9b", "gemma-2-9b", "google/"},
		{"gemma-2-27b", "gemma-2-27b", "google/"},
		{"gemma-3-4b", "gemma-3-4b", "google/"},
		{"palm-2", "palm-2", "google/"},
		{"embedding-001", "embedding-001", "google/"},
		{"embedding-gecko-001", "embedding-gecko-001", "google/"},
		{"text-embedding-004", "text-embedding-004", "google/"},

		// ==================== Anthropic Claude models ====================
		{"claude-2", "claude-2", "anthropic/"},
		{"claude-2.1", "claude-2.1", "anthropic/"},
		{"claude-3-opus", "claude-3-opus", "anthropic/"},
		{"claude-3-opus-20240229", "claude-3-opus-20240229", "anthropic/"},
		{"claude-3-sonnet", "claude-3-sonnet", "anthropic/"},
		{"claude-3-haiku", "claude-3-haiku", "anthropic/"},
		{"claude-3.5-sonnet", "claude-3.5-sonnet", "anthropic/"},
		{"claude-3.5-haiku", "claude-3.5-haiku", "anthropic/"},
		{"claude-3.7-sonnet", "claude-3.7-sonnet", "anthropic/"},
		{"claude-4-opus", "claude-4-opus", "anthropic/"},
		{"claude-4-sonnet", "claude-4-sonnet", "anthropic/"},
		{"claude-opus-4.5", "claude-opus-4.5", "anthropic/"},

		// ==================== GLM models (Zhipu AI) ====================
		{"glm-4", "glm-4", "glm/"},
		{"glm-4-flash", "glm-4-flash", "glm/"},
		{"glm-4.5", "glm-4.5", "glm/"},
		{"glm-4.6", "glm-4.6", "glm/"},
		{"glm-4.7", "glm-4.7", "glm/"},
		{"chatglm-turbo", "chatglm-turbo", "glm/"},
		{"chatglm3-6b", "chatglm3-6b", "glm/"},
		{"cogview-3", "cogview-3", "glm/"},
		{"cogvideo-x", "cogvideo-x", "glm/"},

		// ==================== Kimi/Moonshot models ====================
		{"kimi-chat", "kimi-chat", "kimi/"},
		{"kimi-k2", "kimi-k2", "kimi/"},
		{"kimi-k2-thinking", "kimi-k2-thinking", "kimi/"},
		{"moonshot-v1-8k", "moonshot-v1-8k", "kimi/"},
		{"moonshot-v1-32k", "moonshot-v1-32k", "kimi/"},
		{"moonshot-v1-128k", "moonshot-v1-128k", "kimi/"},

		// ==================== Qwen/Tongyi models (Alibaba) ====================
		{"qwen-turbo", "qwen-turbo", "tongyi/"},
		{"qwen-plus", "qwen-plus", "tongyi/"},
		{"qwen-max", "qwen-max", "tongyi/"},
		{"qwen-long", "qwen-long", "tongyi/"},
		{"qwen2-72b", "qwen2-72b", "tongyi/"},
		{"qwen2.5-72b", "qwen2.5-72b", "tongyi/"},
		{"qwen2.5-coder-32b", "qwen2.5-coder-32b", "tongyi/"},
		{"qwen2.5-max", "qwen2.5-max", "tongyi/"},
		{"qwen3-235b", "qwen3-235b", "tongyi/"},
		{"qwen3-235b-a22b", "qwen3-235b-a22b", "tongyi/"},
		{"qwq-32b", "qwq-32b", "tongyi/"},
		{"qwq-32b-preview", "qwq-32b-preview", "tongyi/"},
		{"wanx-v1", "wanx-v1", "tongyi/"},
		{"wan2.6-image", "wan2.6-image", "tongyi/"},
		{"qwen-image-plus", "qwen-image-plus", "tongyi/"},
		{"qwen-image-max", "qwen-image-max", "tongyi/"},

		// ==================== Meta Llama models ====================
		{"llama-2-7b", "llama-2-7b", "meta/"},
		{"llama-2-13b", "llama-2-13b", "meta/"},
		{"llama-2-70b", "llama-2-70b", "meta/"},
		{"llama-3-8b", "llama-3-8b", "meta/"},
		{"llama-3-70b", "llama-3-70b", "meta/"},
		{"llama-3.1-8b", "llama-3.1-8b", "meta/"},
		{"llama-3.1-70b", "llama-3.1-70b", "meta/"},
		{"llama-3.1-405b", "llama-3.1-405b", "meta/"},
		{"llama-3.2-1b", "llama-3.2-1b", "meta/"},
		{"llama-3.2-3b", "llama-3.2-3b", "meta/"},
		{"llama-3.3-70b", "llama-3.3-70b", "meta/"},
		{"llama-4-scout", "llama-4-scout", "meta/"},
		{"llama-4-maverick", "llama-4-maverick", "meta/"},
		{"codellama-7b", "codellama-7b", "meta/"},
		{"codellama-34b", "codellama-34b", "meta/"},
		{"codellama-70b", "codellama-70b", "meta/"},

		// ==================== Mistral models ====================
		{"mistral-7b", "mistral-7b", "mistral/"},
		{"mistral-7b-instruct", "mistral-7b-instruct", "mistral/"},
		{"mistral-small", "mistral-small", "mistral/"},
		{"mistral-small-3", "mistral-small-3", "mistral/"},
		{"mistral-small-3.1", "mistral-small-3.1", "mistral/"},
		{"mistral-small-3.2", "mistral-small-3.2", "mistral/"},
		{"mistral-medium", "mistral-medium", "mistral/"},
		{"mistral-medium-3", "mistral-medium-3", "mistral/"},
		{"mistral-medium-3.1", "mistral-medium-3.1", "mistral/"},
		{"mistral-large", "mistral-large", "mistral/"},
		{"mistral-large-2", "mistral-large-2", "mistral/"},
		{"mistral-large-3", "mistral-large-3", "mistral/"},
		{"mixtral-8x7b", "mixtral-8x7b", "mistral/"},
		{"mixtral-8x22b", "mixtral-8x22b", "mistral/"},
		{"codestral-latest", "codestral-latest", "mistral/"},
		{"codestral-mamba", "codestral-mamba", "mistral/"},
		{"pixtral-12b", "pixtral-12b", "mistral/"},
		{"pixtral-large", "pixtral-large", "mistral/"},
		{"magistral-small", "magistral-small", "mistral/"},
		{"magistral-medium", "magistral-medium", "mistral/"},
		{"devstral-2", "devstral-2", "mistral/"},
		{"ministral-3-14b", "ministral-3-14b", "mistral/"},
		{"ministral-8b", "ministral-8b", "mistral/"},

		// ==================== Cohere models ====================
		{"command-r", "command-r", "cohere/"},
		{"command-r-plus", "command-r-plus", "cohere/"},
		{"command-r7b", "command-r7b", "cohere/"},
		{"command-light", "command-light", "cohere/"},
		{"c4ai-aya-23", "c4ai-aya-23", "cohere/"},
		{"c4ai-aya-expanse", "c4ai-aya-expanse", "cohere/"},
		{"aya-expanse-8b", "aya-expanse-8b", "cohere/"},
		{"aya-expanse-32b", "aya-expanse-32b", "cohere/"},

		// ==================== xAI Grok models ====================
		{"grok-1", "grok-1", "xai/"},
		{"grok-2", "grok-2", "xai/"},
		{"grok-2-mini", "grok-2-mini", "xai/"},
		{"grok-3", "grok-3", "xai/"},
		{"grok-4", "grok-4", "xai/"},
		{"grok-beta", "grok-beta", "xai/"},

		// ==================== Amazon models ====================
		{"nova-pro", "nova-pro", "amazon/"},
		{"nova-lite", "nova-lite", "amazon/"},
		{"nova-micro", "nova-micro", "amazon/"},
		{"titan-text-express", "titan-text-express", "amazon/"},
		{"titan-text-lite", "titan-text-lite", "amazon/"},
		{"titan-embed-text", "titan-embed-text", "amazon/"},
		{"amazon-titan-tg1-large", "amazon-titan-tg1-large", "amazon/"},

		// ==================== Microsoft Phi models ====================
		{"phi-2", "phi-2", "microsoft/"},
		{"phi-3-mini", "phi-3-mini", "microsoft/"},
		{"phi-3-small", "phi-3-small", "microsoft/"},
		{"phi-3-medium", "phi-3-medium", "microsoft/"},
		{"phi-3.5-mini", "phi-3.5-mini", "microsoft/"},
		{"phi-4", "phi-4", "microsoft/"},
		{"phi-4-mini-flash", "phi-4-mini-flash", "microsoft/"},

		// ==================== Yi models (01.AI) ====================
		{"yi-6b", "yi-6b", "yi/"},
		{"yi-34b", "yi-34b", "yi/"},
		{"yi-large", "yi-large", "yi/"},
		{"yi-lightning", "yi-lightning", "yi/"},
		{"yi-vision", "yi-vision", "yi/"},

		// ==================== Baichuan models ====================
		{"baichuan-7b", "baichuan-7b", "baichuan/"},
		{"baichuan-13b", "baichuan-13b", "baichuan/"},
		{"baichuan2-7b", "baichuan2-7b", "baichuan/"},
		{"baichuan2-13b", "baichuan2-13b", "baichuan/"},
		{"baichuan2-53b", "baichuan2-53b", "baichuan/"},

		// ==================== MiniMax models ====================
		{"minimax-abab5", "minimax-abab5", "minimax/"},
		{"minimax-abab6", "minimax-abab6", "minimax/"},
		{"abab5.5-chat", "abab5.5-chat", "minimax/"},
		{"abab6.5-chat", "abab6.5-chat", "minimax/"},
		{"minimax-m2.1", "minimax-m2.1", "minimax/"},

		// ==================== Doubao/ByteDance models ====================
		{"doubao-pro", "doubao-pro", "doubao/"},
		{"doubao-lite", "doubao-lite", "doubao/"},
		{"doubao-pro-32k", "doubao-pro-32k", "doubao/"},
		{"skylark-chat", "skylark-chat", "doubao/"},
		{"skylark-pro", "skylark-pro", "doubao/"},

		// ==================== Hunyuan/Tencent models ====================
		{"hunyuan-lite", "hunyuan-lite", "tencent/"},
		{"hunyuan-standard", "hunyuan-standard", "tencent/"},
		{"hunyuan-pro", "hunyuan-pro", "tencent/"},
		{"hunyuan-turbo", "hunyuan-turbo", "tencent/"},

		// ==================== Spark/iFlytek models ====================
		{"spark-v3", "spark-v3", "spark/"},
		{"spark-v3.5", "spark-v3.5", "spark/"},
		{"spark-v4", "spark-v4", "spark/"},
		{"spark-lite", "spark-lite", "spark/"},
		{"spark-pro", "spark-pro", "spark/"},
		{"spark-max", "spark-max", "spark/"},

		// ==================== ERNIE/Baidu models ====================
		{"ernie-3.5", "ernie-3.5", "baidu/"},
		{"ernie-4.0", "ernie-4.0", "baidu/"},
		{"ernie-4.0-turbo", "ernie-4.0-turbo", "baidu/"},
		{"ernie-bot", "ernie-bot", "baidu/"},
		{"ernie-bot-turbo", "ernie-bot-turbo", "baidu/"},
		{"wenxin-4", "wenxin-4", "baidu/"},

		// ==================== Stability AI models ====================
		{"stable-diffusion-xl", "stable-diffusion-xl", "stability/"},
		{"stable-diffusion-3", "stable-diffusion-3", "stability/"},
		{"sdxl-turbo", "sdxl-turbo", "stability/"},
		{"sd3-turbo", "sd3-turbo", "stability/"},
		{"stability-ai-sdxl", "stability-ai-sdxl", "stability/"},

		// ==================== Perplexity models ====================
		{"sonar-small", "sonar-small", "perplexity/"},
		{"sonar-medium", "sonar-medium", "perplexity/"},
		{"sonar-large", "sonar-large", "perplexity/"},
		{"sonar-pro", "sonar-pro", "perplexity/"},
		{"pplx-7b", "pplx-7b", "perplexity/"},
		{"pplx-70b", "pplx-70b", "perplexity/"},
		{"perplexity-online", "perplexity-online", "perplexity/"},

		// ==================== Other popular models ====================
		{"falcon-7b", "falcon-7b", "tii/"},
		{"falcon-40b", "falcon-40b", "tii/"},
		{"falcon-180b", "falcon-180b", "tii/"},
		{"internlm-7b", "internlm-7b", "internlm/"},
		{"internlm2-7b", "internlm2-7b", "internlm/"},
		{"internlm2-20b", "internlm2-20b", "internlm/"},
		{"internvl-chat", "internvl-chat", "internlm/"},
		{"hermes-3-llama-3.1-8b", "hermes-3-llama-3.1-8b", "nous/"},
		{"nous-hermes-2", "nous-hermes-2", "nous/"},
		{"wizardlm-7b", "wizardlm-7b", "wizardlm/"},
		{"wizardlm-13b", "wizardlm-13b", "wizardlm/"},
		{"wizardlm-2-8x22b", "wizardlm-2-8x22b", "wizardlm/"},
		{"wizardcoder-15b", "wizardcoder-15b", "wizardlm/"},
		{"vicuna-7b", "vicuna-7b", "lmsys/"},
		{"vicuna-13b", "vicuna-13b", "lmsys/"},
		{"vicuna-33b", "vicuna-33b", "lmsys/"},
		{"openchat-3.5", "openchat-3.5", "openchat/"},
		{"openchat-3.6", "openchat-3.6", "openchat/"},
		{"zephyr-7b", "zephyr-7b", "huggingface/"},
		{"zephyr-7b-beta", "zephyr-7b-beta", "huggingface/"},
		{"starcoder", "starcoder", "bigcode/"},
		{"starcoder2-3b", "starcoder2-3b", "bigcode/"},
		{"starcoder2-7b", "starcoder2-7b", "bigcode/"},
		{"starcoder2-15b", "starcoder2-15b", "bigcode/"},
		{"starchat-beta", "starchat-beta", "bigcode/"},
		{"codegen-16b", "codegen-16b", "salesforce/"},
		{"codegen2-16b", "codegen2-16b", "salesforce/"},
		{"nemotron-4-340b", "nemotron-4-340b", "nvidia/"},
		{"nemotron-mini-4b", "nemotron-mini-4b", "nvidia/"},
		{"jamba-1.5-mini", "jamba-1.5-mini", "ai21/"},
		{"jamba-1.5-large", "jamba-1.5-large", "ai21/"},
		{"jurassic-2-ultra", "jurassic-2-ultra", "ai21/"},
		{"ai21-jamba-instruct", "ai21-jamba-instruct", "ai21/"},
		{"dbrx-instruct", "dbrx-instruct", "databricks/"},
		{"dbrx-base", "dbrx-base", "databricks/"},
		{"arctic-instruct", "arctic-instruct", "snowflake/"},
		{"arctic-embed", "arctic-embed", "snowflake/"},
		{"snowflake-arctic-embed", "snowflake-arctic-embed", "snowflake/"},
		{"groq-llama3-8b", "groq-llama3-8b", "groq/"},
		{"groq-mixtral-8x7b", "groq-mixtral-8x7b", "groq/"},
		{"together-llama-3-70b", "together-llama-3-70b", "together/"},
		{"reka-core", "reka-core", "reka/"},
		{"reka-flash", "reka-flash", "reka/"},
		{"reka-edge", "reka-edge", "reka/"},

		// ==================== BAAI BGE Embedding/Reranking models ====================
		{"bge-large-en-v1.5", "bge-large-en-v1.5", "baai/"},
		{"bge-base-en-v1.5", "bge-base-en-v1.5", "baai/"},
		{"bge-small-en-v1.5", "bge-small-en-v1.5", "baai/"},
		{"bge-m3", "bge-m3", "baai/"},
		{"bge-reranker-base", "bge-reranker-base", "baai/"},
		{"bge-reranker-large", "bge-reranker-large", "baai/"},
		{"bge-reranker-v2-m3", "bge-reranker-v2-m3", "baai/"},
		{"bge-reranker-v2-gemma", "bge-reranker-v2-gemma", "baai/"},
		{"bge-multilingual-gemma2", "bge-multilingual-gemma2", "baai/"},

		// ==================== Jina AI Embedding/Reranking models ====================
		{"jina-embeddings-v2-base-en", "jina-embeddings-v2-base-en", "jina/"},
		{"jina-embeddings-v3", "jina-embeddings-v3", "jina/"},
		{"jina-embeddings-v4", "jina-embeddings-v4", "jina/"},
		{"jina-reranker-v1-base-en", "jina-reranker-v1-base-en", "jina/"},
		{"jina-reranker-v2-base-multilingual", "jina-reranker-v2-base-multilingual", "jina/"},
		{"jina-reranker-m0", "jina-reranker-m0", "jina/"},
		{"jina-clip-v1", "jina-clip-v1", "jina/"},
		{"jina-clip-v2", "jina-clip-v2", "jina/"},
		{"jina-colbert-v2", "jina-colbert-v2", "jina/"},
		{"jina-vlm-2b", "jina-vlm-2b", "jina/"},

		// ==================== Voyage AI Embedding models ====================
		{"voyage-3", "voyage-3", "voyage/"},
		{"voyage-3-large", "voyage-3-large", "voyage/"},
		{"voyage-3.5", "voyage-3.5", "voyage/"},
		{"voyage-3.5-lite", "voyage-3.5-lite", "voyage/"},
		{"voyage-4", "voyage-4", "voyage/"},
		{"voyage-4-large", "voyage-4-large", "voyage/"},
		{"voyage-4-lite", "voyage-4-lite", "voyage/"},
		{"voyage-code-2", "voyage-code-2", "voyage/"},
		{"voyage-code-3", "voyage-code-3", "voyage/"},
		{"voyage-finance-2", "voyage-finance-2", "voyage/"},
		{"voyage-law-2", "voyage-law-2", "voyage/"},
		{"voyage-multimodal-3", "voyage-multimodal-3", "voyage/"},
		{"voyage-multimodal-3.5", "voyage-multimodal-3.5", "voyage/"},

		// ==================== Nomic AI Embedding models ====================
		{"nomic-embed-text-v1", "nomic-embed-text-v1", "nomic/"},
		{"nomic-embed-text-v1.5", "nomic-embed-text-v1.5", "nomic/"},

		// ==================== Mixedbread AI Reranking models ====================
		{"mxbai-rerank-base-v2", "mxbai-rerank-base-v2", "mixedbread/"},
		{"mxbai-embed-large-v1", "mxbai-embed-large-v1", "mixedbread/"},

		// ==================== Alibaba GTE Embedding models ====================
		{"gte-large-en-v1.5", "gte-large-en-v1.5", "alibaba/"},
		{"gte-multilingual-base", "gte-multilingual-base", "alibaba/"},
		{"gte-multilingual-reranker-base", "gte-multilingual-reranker-base", "alibaba/"},
		{"gte-modernbert-base", "gte-modernbert-base", "alibaba/"},

		// ==================== Video Generation models ====================
		{"sora", "sora", "openai/"},
		{"sora-2", "sora-2", "openai/"},
		{"sora-2-pro", "sora-2-pro", "openai/"},
		{"veo-2", "veo-2", "google/"},
		{"veo-3", "veo-3", "google/"},
		{"veo-3.1", "veo-3.1", "google/"},
		{"runway-gen-3", "runway-gen-3", "runway/"},
		{"gen-3-alpha", "gen-3-alpha", "runway/"},
		{"gen-4", "gen-4", "runway/"},
		{"gen-4.5", "gen-4.5", "runway/"},
		{"pika-1.0", "pika-1.0", "pika/"},
		{"pika-2.0", "pika-2.0", "pika/"},
		{"pika-2.1", "pika-2.1", "pika/"},
		{"pika-2.2", "pika-2.2", "pika/"},
		{"kling-1.5", "kling-1.5", "kuaishou/"},
		{"kling-2.0", "kling-2.0", "kuaishou/"},
		{"kling-2.5", "kling-2.5", "kuaishou/"},
		{"luma-dream-machine", "luma-dream-machine", "luma/"},
		{"dream-machine-1.5", "dream-machine-1.5", "luma/"},
		{"ray-2", "ray-2", "luma/"},
		{"ray-3", "ray-3", "luma/"},
		{"hailuo-01", "hailuo-01", "minimax/"},
		{"hailuo-02", "hailuo-02", "minimax/"},
		{"seedance-1.0", "seedance-1.0", "bytedance/"},
		{"pixverse-v4", "pixverse-v4", "pixverse/"},
		{"pixverse-v4.5", "pixverse-v4.5", "pixverse/"},

		// ==================== Image Generation models ====================
		{"flux-pro", "flux-pro", "bfl/"},
		{"flux-dev", "flux-dev", "bfl/"},
		{"flux-schnell", "flux-schnell", "bfl/"},
		{"flux-1.1-pro", "flux-1.1-pro", "bfl/"},
		{"flux-1.1-pro-ultra", "flux-1.1-pro-ultra", "bfl/"},
		{"flux-2-dev", "flux-2-dev", "bfl/"},
		{"flux-kontext-dev", "flux-kontext-dev", "bfl/"},
		{"flux-fill-dev", "flux-fill-dev", "bfl/"},
		{"flux-redux-dev", "flux-redux-dev", "bfl/"},
		{"midjourney-v6", "midjourney-v6", "midjourney/"},
		{"mj-v6.1", "mj-v6.1", "midjourney/"},
		{"ideogram-v2", "ideogram-v2", "ideogram/"},
		{"ideogram-v3", "ideogram-v3", "ideogram/"},
		{"leonardo-diffusion-xl", "leonardo-diffusion-xl", "leonardo/"},

		// ==================== Audio/Speech models ====================
		{"eleven_multilingual_v2", "eleven_multilingual_v2", "elevenlabs/"},
		{"eleven_turbo_v2", "eleven_turbo_v2", "elevenlabs/"},
		{"eleven_turbo_v2.5", "eleven_turbo_v2.5", "elevenlabs/"},
		{"eleven_flash_v2", "eleven_flash_v2", "elevenlabs/"},
		{"eleven_flash_v2.5", "eleven_flash_v2.5", "elevenlabs/"},
		{"elevenlabs-v3", "elevenlabs-v3", "elevenlabs/"},

		// ==================== API Models from api.5202030.xyz ====================
		// Claude models
		{"claude-3.5-haiku", "claude-3.5-haiku", "anthropic/"},
		{"claude-3.5-sonnet", "claude-3.5-sonnet", "anthropic/"},
		{"claude-3.7-sonnet", "claude-3.7-sonnet", "anthropic/"},
		{"claude-4-opus", "claude-4-opus", "anthropic/"},
		{"claude-4-sonnet", "claude-4-sonnet", "anthropic/"},
		{"claude-4-sonnet-think", "claude-4-sonnet-think", "anthropic/"},
		{"claude-4.1-opus", "claude-4.1-opus", "anthropic/"},
		{"claude-4.5-sonnet", "claude-4.5-sonnet", "anthropic/"},
		{"claude-haiku-4-5-20251001", "claude-haiku-4-5-20251001", "anthropic/"},
		{"claude-opus-4-1-20250805", "claude-opus-4-1-20250805", "anthropic/"},
		{"claude-opus-4-5-20251101", "claude-opus-4-5-20251101", "anthropic/"},
		{"claude-sonnet-4-20250514", "claude-sonnet-4-20250514", "anthropic/"},
		{"claude-sonnet-4-20250514-think", "claude-sonnet-4-20250514-think", "anthropic/"},
		{"claude-sonnet-4-5-20250929", "claude-sonnet-4-5-20250929", "anthropic/"},

		// DeepSeek models
		{"deepseek-ai/DeepSeek-R1-0528", "deepseek-ai/DeepSeek-R1-0528", "deepseek/"},
		{"deepseek-ai/DeepSeek-R1-0528-Turbo", "deepseek-ai/DeepSeek-R1-0528-Turbo", "deepseek/"},
		{"deepseek-ai/DeepSeek-V3-0324", "deepseek-ai/DeepSeek-V3-0324", "deepseek/"},
		{"deepseek-ai/DeepSeek-V3-0324-Turbo", "deepseek-ai/DeepSeek-V3-0324-Turbo", "deepseek/"},
		{"deepseek-ai/deepseek-v3.1", "deepseek-ai/deepseek-v3.1", "deepseek/"},
		{"deepseek-ai/deepseek-v3.1-terminus", "deepseek-ai/deepseek-v3.1-terminus", "deepseek/"},
		{"deepseek-ai/DeepSeek-V3.1", "deepseek-ai/DeepSeek-V3.1", "deepseek/"},
		{"deepseek-ai/DeepSeek-V3.2", "deepseek-ai/DeepSeek-V3.2", "deepseek/"},
		{"deepseek-ai/deepseek-v3.2", "deepseek-ai/deepseek-v3.2", "deepseek/"},
		{"deepseek-r1", "deepseek-r1", "deepseek/"},
		{"deepseek-r1-0528", "deepseek-r1-0528", "deepseek/"},
		{"deepseek-v3", "deepseek-v3", "deepseek/"},
		{"deepseek-v3.1", "deepseek-v3.1", "deepseek/"},
		{"deepseek-v3.1-terminus", "deepseek-v3.1-terminus", "deepseek/"},
		{"deepseek-v3.2", "deepseek-v3.2", "deepseek/"},
		{"deepseek-v3.2-chat", "deepseek-v3.2-chat", "deepseek/"},
		{"deepseek-v3.2-reasoner", "deepseek-v3.2-reasoner", "deepseek/"},

		// Gemini models
		{"gemini-2.0-flash", "gemini-2.0-flash", "google/"},
		{"gemini-2.5-flash", "gemini-2.5-flash", "google/"},
		{"gemini-2.5-flash-lite", "gemini-2.5-flash-lite", "google/"},
		{"gemini-2.5-flash-maxthinking", "gemini-2.5-flash-maxthinking", "google/"},
		{"gemini-2.5-flash-maxthinking-search", "gemini-2.5-flash-maxthinking-search", "google/"},
		{"gemini-2.5-flash-nothinking", "gemini-2.5-flash-nothinking", "google/"},
		{"gemini-2.5-flash-nothinking-search", "gemini-2.5-flash-nothinking-search", "google/"},
		{"gemini-2.5-flash-search", "gemini-2.5-flash-search", "google/"},
		{"gemini-2.5-pro", "gemini-2.5-pro", "google/"},
		{"gemini-2.5-pro-maxthinking", "gemini-2.5-pro-maxthinking", "google/"},
		{"gemini-2.5-pro-maxthinking-search", "gemini-2.5-pro-maxthinking-search", "google/"},
		{"gemini-2.5-pro-nothinking", "gemini-2.5-pro-nothinking", "google/"},
		{"gemini-2.5-pro-nothinking-search", "gemini-2.5-pro-nothinking-search", "google/"},
		{"gemini-2.5-pro-search", "gemini-2.5-pro-search", "google/"},
		{"gemini-3-flash-preview", "gemini-3-flash-preview", "google/"},
		{"gemini-3-flash-preview-maxthinking", "gemini-3-flash-preview-maxthinking", "google/"},
		{"gemini-3-flash-preview-maxthinking-search", "gemini-3-flash-preview-maxthinking-search", "google/"},
		{"gemini-3-flash-preview-nothinking", "gemini-3-flash-preview-nothinking", "google/"},
		{"gemini-3-flash-preview-nothinking-search", "gemini-3-flash-preview-nothinking-search", "google/"},
		{"gemini-3-flash-preview-search", "gemini-3-flash-preview-search", "google/"},
		{"gemini-3-pro-preview", "gemini-3-pro-preview", "google/"},
		{"gemini-3-pro-preview-maxthinking", "gemini-3-pro-preview-maxthinking", "google/"},
		{"gemini-3-pro-preview-maxthinking-search", "gemini-3-pro-preview-maxthinking-search", "google/"},
		{"gemini-3-pro-preview-nothinking", "gemini-3-pro-preview-nothinking", "google/"},
		{"gemini-3-pro-preview-nothinking-search", "gemini-3-pro-preview-nothinking-search", "google/"},
		{"gemini-3-pro-preview-search", "gemini-3-pro-preview-search", "google/"},

		// GLM models
		{"glm-4.6", "glm-4.6", "glm/"},
		{"glm-4.7", "glm-4.7", "glm/"},
		{"zai-org/GLM-4.5", "zai-org/GLM-4.5", "glm/"},
		{"z-ai/glm4.7", "z-ai/glm4.7", "glm/"},
		{"zhipu/glm-4-flash", "zhipu/glm-4-flash", "glm/"},
		{"zhipu/glm-4.5-flash", "zhipu/glm-4.5-flash", "glm/"},
		{"zhipu/glm-4v-flash", "zhipu/glm-4v-flash", "glm/"},

		// GPT models
		{"gpt-4-turbo", "gpt-4-turbo", "openai/"},
		{"gpt-4-vision-preview", "gpt-4-vision-preview", "openai/"},
		{"gpt-4.1", "gpt-4.1", "openai/"},
		{"gpt-4o", "gpt-4o", "openai/"},
		{"gpt-4o-2024-08-06", "gpt-4o-2024-08-06", "openai/"},
		{"gpt-4o-mini", "gpt-4o-mini", "openai/"},
		{"gpt-4o-mini-2024-07-18", "gpt-4o-mini-2024-07-18", "openai/"},
		{"gpt-5", "gpt-5", "openai/"},
		{"gpt-5-codex", "gpt-5-codex", "openai/"},
		{"gpt-5-mini", "gpt-5-mini", "openai/"},
		{"gpt-5-nano", "gpt-5-nano", "openai/"},
		{"gpt-5.1", "gpt-5.1", "openai/"},
		{"gpt-5.1-codex", "gpt-5.1-codex", "openai/"},
		{"gpt-5.1-codex-max", "gpt-5.1-codex-max", "openai/"},
		{"gpt-5.1-codex-mini", "gpt-5.1-codex-mini", "openai/"},
		{"gpt-5.2", "gpt-5.2", "openai/"},
		{"gpt-5.2-codex", "gpt-5.2-codex", "openai/"},
		{"openai/gpt-oss-120b", "openai/gpt-oss-120b", "openai/"},
		{"openai/gpt-oss-20b", "openai/gpt-oss-20b", "openai/"},
		{"dall-e-3", "dall-e-3", "openai/"},
		{"o3", "o3", "openai/"},
		{"o4-mini", "o4-mini", "openai/"},

		// Grok models
		{"grok-3", "grok-3", "xai/"},
		{"grok-3-mini", "grok-3-mini", "xai/"},
		{"grok-4", "grok-4", "xai/"},
		{"grok-imagine-fun", "grok-imagine-fun", "xai/"},
		{"grok-imagine-normal", "grok-imagine-normal", "xai/"},
		{"grok-imagine-spicy", "grok-imagine-spicy", "xai/"},

		// Kimi models
		{"kimi-k2", "kimi-k2", "kimi/"},
		{"kimi-k2-0905", "kimi-k2-0905", "kimi/"},
		{"kimi-k2-instruct", "kimi-k2-instruct", "kimi/"},
		{"kimi-k2-instruct-0905", "kimi-k2-instruct-0905", "kimi/"},
		{"kimi-k2-thinking", "kimi-k2-thinking", "kimi/"},
		{"moonshotai/kimi-k2-instruct", "moonshotai/kimi-k2-instruct", "kimi/"},
		{"moonshotai/Kimi-K2-Instruct", "moonshotai/Kimi-K2-Instruct", "kimi/"},
		{"moonshotai/Kimi-K2-Instruct-0905", "moonshotai/Kimi-K2-Instruct-0905", "kimi/"},
		{"moonshotai/kimi-k2-instruct-0905", "moonshotai/kimi-k2-instruct-0905", "kimi/"},
		{"moonshotai/kimi-k2-thinking", "moonshotai/kimi-k2-thinking", "kimi/"},

		// Llama models
		{"meta-llama/Llama-4-Maverick-17B-128E-Instruct-Turbo", "meta-llama/Llama-4-Maverick-17B-128E-Instruct-Turbo", "meta/"},
		{"tokyotech-llm/llama-3-swallow-70b-instruct-v0.1", "tokyotech-llm/llama-3-swallow-70b-instruct-v0.1", "meta/"},

		// MiniMax models
		{"minimax-m2", "minimax-m2", "minimax/"},
		{"minimax-m2.1", "minimax-m2.1", "minimax/"},
		{"MiniMax/MiniMax-M2.1", "MiniMax/MiniMax-M2.1", "minimax/"},
		{"minimaxai/minimax-m2", "minimaxai/minimax-m2", "minimax/"},
		{"minimaxai/minimax-m2.1", "minimaxai/minimax-m2.1", "minimax/"},

		// Qwen models
		{"Qwen/Qwen3-235B-A22B-Instruct-2507", "Qwen/Qwen3-235B-A22B-Instruct-2507", "tongyi/"},
		{"Qwen/Qwen3-Coder-30B-A3B-Instruct", "Qwen/Qwen3-Coder-30B-A3B-Instruct", "tongyi/"},
		{"Qwen/Qwen3-Coder-480B-A35B-Instruct", "Qwen/Qwen3-Coder-480B-A35B-Instruct", "tongyi/"},
		{"Qwen/Qwen3-Coder-480B-A35B-Instruct-Turbo", "Qwen/Qwen3-Coder-480B-A35B-Instruct-Turbo", "tongyi/"},
		{"qwen3-235b", "qwen3-235b", "tongyi/"},
		{"qwen3-235b-a22b-instruct", "qwen3-235b-a22b-instruct", "tongyi/"},
		{"qwen3-235b-a22b-thinking-2507", "qwen3-235b-a22b-thinking-2507", "tongyi/"},
		{"qwen3-32b", "qwen3-32b", "tongyi/"},
		{"qwen3-coder-flash", "qwen3-coder-flash", "tongyi/"},
		{"qwen3-coder-plus", "qwen3-coder-plus", "tongyi/"},
		{"qwen3-max", "qwen3-max", "tongyi/"},
		{"qwen3-max-preview", "qwen3-max-preview", "tongyi/"},
		{"qwen3-vl-plus", "qwen3-vl-plus", "tongyi/"},

		// Snowflake models
		{"snowflake/arctic-embed-l", "snowflake/arctic-embed-l", "snowflake/"},

		// NVIDIA models
		{"nvidia/embed-qa-4", "nvidia/embed-qa-4", "nvidia/"},

		// Xiaomi MiMo models
		{"XiaomiMiMo/MiMo-V2-Flash", "XiaomiMiMo/MiMo-V2-Flash", "xiaomi/"},
		{"mimo-v2-flash", "mimo-v2-flash", "xiaomi/"},
		{"MiMo-V2-Flash", "MiMo-V2-Flash", "xiaomi/"},

		// Meituan LongCat models
		{"LongCat-Flash-Chat", "LongCat-Flash-Chat", "meituan/"},
		{"LongCat-Flash-Thinking", "LongCat-Flash-Thinking", "meituan/"},
		{"longcat-flash-chat", "longcat-flash-chat", "meituan/"},
		{"longcat-flash-thinking", "longcat-flash-thinking", "meituan/"},
		{"meituan-longcat/LongCat-Flash-Thinking-2601", "meituan-longcat/LongCat-Flash-Thinking-2601", "meituan/"},

		// Menlo/Jan models
		{"jan-nano", "jan-nano", "menlo/"},
		{"jan-nano-128k", "jan-nano-128k", "menlo/"},
		{"menlo/jan-nano", "menlo/jan-nano", "menlo/"},

		// StepFun models (阶跃星辰)
		{"step3", "step3", "stepfun/"},
		{"step-3", "step-3", "stepfun/"},
		{"step3-vl-10b", "step3-vl-10b", "stepfun/"},
		{"step-3-fp8", "step-3-fp8", "stepfun/"},
		{"stepfun-ai/step3", "stepfun-ai/step3", "stepfun/"},

		// OpenCompass models
		{"compassjudger-1-32b-instruct", "compassjudger-1-32b-instruct", "opencompass/"},
		{"opencompass/compassjudger-1-32b-instruct", "opencompass/compassjudger-1-32b-instruct", "opencompass/"},

		// InternLM/InternVL models
		{"internvl3_5-241b-a28b", "internvl3_5-241b-a28b", "internlm/"},
		{"internlm/internvl3_5-241b-a28b", "internlm/internvl3_5-241b-a28b", "internlm/"},

		// MiniMax models with different formats
		{"minimax-m1-80k", "minimax-m1-80k", "minimax/"},

		// ByteDance Seed models (lowercase)
		{"seed-oss-36b-instruct", "seed-oss-36b-instruct", "bytedance/"},
		{"seed-oss-36b", "seed-oss-36b", "bytedance/"},
		{"seed-1-thinking", "seed-1-thinking", "bytedance/"},
		{"seed-2-pro", "seed-2-pro", "bytedance/"},
		// ByteDance Seed models (mixed case)
		{"Seed-OSS-36B-Instruct", "Seed-OSS-36B-Instruct", "bytedance/"},
		{"SEED-OSS-36B", "SEED-OSS-36B", "bytedance/"},
		// ByteDance Seed models (with prefix)
		{"bytedance-seed/seed-oss-36b-instruct", "bytedance-seed/seed-oss-36b-instruct", "bytedance/"},
		{"ByteDance-Seed/Seed-OSS-36B-Instruct", "ByteDance-Seed/Seed-OSS-36B-Instruct", "bytedance/"},

		// FunAudioLLM/SenseVoice models (Alibaba Tongyi) - lowercase
		{"sensevoicesmall", "sensevoicesmall", "tongyi/"},
		{"sensevoicelarge", "sensevoicelarge", "tongyi/"},
		{"funaudiollm-sensevoice", "funaudiollm-sensevoice", "tongyi/"},
		// FunAudioLLM/SenseVoice models - mixed case
		{"SenseVoiceSmall", "SenseVoiceSmall", "tongyi/"},
		{"SenseVoice-Large", "SenseVoice-Large", "tongyi/"},
		{"FunAudioLLM-SenseVoice", "FunAudioLLM-SenseVoice", "tongyi/"},
		// FunAudioLLM/SenseVoice models - with prefix
		{"funaudiollm/sensevoicesmall", "funaudiollm/sensevoicesmall", "tongyi/"},
		{"FunAudioLLM/SenseVoiceSmall", "FunAudioLLM/SenseVoiceSmall", "tongyi/"},

		// Kuaishou Kolors image models - lowercase
		{"kolors", "kolors", "kuaishou/"},
		{"kolors-v1", "kolors-v1", "kuaishou/"},
		{"kolors-diffusion", "kolors-diffusion", "kuaishou/"},
		{"kling-1.5", "kling-1.5", "kuaishou/"},
		{"kling-2.0", "kling-2.0", "kuaishou/"},
		// Kuaishou Kolors image models - mixed case
		{"Kolors", "Kolors", "kuaishou/"},
		{"Kolors-V1", "Kolors-V1", "kuaishou/"},
		{"KOLORS", "KOLORS", "kuaishou/"},
		{"Kling-2.0", "Kling-2.0", "kuaishou/"},
		// Kuaishou Kolors image models - with prefix
		{"kwai-kolors/kolors", "kwai-kolors/kolors", "kuaishou/"},
		{"Kwai-Kolors/Kolors", "Kwai-Kolors/Kolors", "kuaishou/"},

		// NetEase Youdao BCE embedding/reranker models - lowercase
		{"bce-embedding-base_v1", "bce-embedding-base_v1", "youdao/"},
		{"bce-reranker-base_v1", "bce-reranker-base_v1", "youdao/"},
		{"bce-embedding-large", "bce-embedding-large", "youdao/"},
		// NetEase Youdao BCE models - mixed case
		{"BCE-Embedding-Base_v1", "BCE-Embedding-Base_v1", "youdao/"},
		{"BCE-Reranker-Base_v1", "BCE-Reranker-Base_v1", "youdao/"},
		// NetEase Youdao BCE models - with prefix
		{"netease-youdao/bce-embedding-base_v1", "netease-youdao/bce-embedding-base_v1", "youdao/"},
		{"netease-youdao/bce-reranker-base_v1", "netease-youdao/bce-reranker-base_v1", "youdao/"},
		{"NetEase-Youdao/BCE-Embedding-Base_v1", "NetEase-Youdao/BCE-Embedding-Base_v1", "youdao/"},

		// THUDM models (Tsinghua University -> GLM) - lowercase
		{"thudm/glm-4-9b-chat", "thudm/glm-4-9b-chat", "glm/"},
		// THUDM models - mixed case
		{"THUDM/GLM-4-9B-Chat", "THUDM/GLM-4-9B-Chat", "glm/"},
		{"Thudm/glm-4-9b-chat", "Thudm/glm-4-9b-chat", "glm/"},

		// Models with lora/ prefix (should be stripped)
		{"lora/qwen/qwen2.5-32b-instruct", "lora/qwen/qwen2.5-32b-instruct", "tongyi/"},
		{"lora/Qwen/Qwen2.5-32B-Instruct", "lora/Qwen/Qwen2.5-32B-Instruct", "tongyi/"},
		{"LORA/qwen/qwen2.5-32b-instruct", "LORA/qwen/qwen2.5-32b-instruct", "tongyi/"},

		// Models with pro/ prefix (should be stripped)
		{"pro/baai/bge-m3", "pro/baai/bge-m3", "baai/"},
		{"pro/baai/bge-reranker-v2-m3", "pro/baai/bge-reranker-v2-m3", "baai/"},
		{"Pro/BAAI/bge-m3", "Pro/BAAI/bge-m3", "baai/"},
		{"PRO/baai/bge-m3", "PRO/baai/bge-m3", "baai/"},

		// Models with free/ prefix (should be stripped)
		{"free/openai/gpt-4", "free/openai/gpt-4", "openai/"},
		{"free/anthropic/claude-3-opus", "free/anthropic/claude-3-opus", "anthropic/"},

		// Models with fast/ prefix (should be stripped)
		{"fast/deepseek/deepseek-chat", "fast/deepseek/deepseek-chat", "deepseek/"},
		{"fast/google/gemini-pro", "fast/google/gemini-pro", "google/"},

		// Huawei Pangu models - lowercase
		{"pangu-pro-moe", "pangu-pro-moe", "huawei/"},
		{"pangu-7b", "pangu-7b", "huawei/"},
		// Huawei Pangu models - mixed case
		{"Pangu-Pro-MoE", "Pangu-Pro-MoE", "huawei/"},
		{"PANGU-PRO-MOE", "PANGU-PRO-MOE", "huawei/"},
		// Huawei Pangu models - with prefix
		{"ascend-tribe/pangu-pro-moe", "ascend-tribe/pangu-pro-moe", "huawei/"},
		{"Ascend-Tribe/Pangu-Pro-MoE", "Ascend-Tribe/Pangu-Pro-MoE", "huawei/"},

		// China Telecom TeleAI models - lowercase
		{"telespeechasr", "telespeechasr", "teleai/"},
		{"telespeech-asr", "telespeech-asr", "teleai/"},
		// China Telecom TeleAI models - mixed case
		{"TeleSpeechASR", "TeleSpeechASR", "teleai/"},
		{"TeleSpeech-ASR", "TeleSpeech-ASR", "teleai/"},
		// China Telecom TeleAI models - with prefix
		{"teleai/telespeechasr", "teleai/telespeechasr", "teleai/"},
		{"TeleAI/TeleSpeechASR", "TeleAI/TeleSpeechASR", "teleai/"},

		// GPT-SoVITS voice cloning models - lowercase
		{"gpt-sovits", "gpt-sovits", "sovits/"},
		{"gpt-sovits-v2", "gpt-sovits-v2", "sovits/"},
		// GPT-SoVITS models - mixed case
		{"GPT-SoVITS", "GPT-SoVITS", "sovits/"},
		{"GPT-SoVITS-V2", "GPT-SoVITS-V2", "sovits/"},
		// GPT-SoVITS models - with prefix
		{"rvc-boss/gpt-sovits", "rvc-boss/gpt-sovits", "sovits/"},
		{"RVC-Boss/GPT-SoVITS", "RVC-Boss/GPT-SoVITS", "sovits/"},

		// Kwaipilot/KAT models (Kuaishou) - lowercase
		{"kat-dev", "kat-dev", "kuaishou/"},
		{"kat-dev-72b-exp", "kat-dev-72b-exp", "kuaishou/"},
		{"kat-v1-40b", "kat-v1-40b", "kuaishou/"},
		// Kwaipilot/KAT models - mixed case
		{"KAT-Dev", "KAT-Dev", "kuaishou/"},
		{"KAT-Dev-72B-Exp", "KAT-Dev-72B-Exp", "kuaishou/"},
		{"KAT-V1-40B", "KAT-V1-40B", "kuaishou/"},
		// Kwaipilot/KAT models - with prefix
		{"kwaipilot/kat-dev", "kwaipilot/kat-dev", "kuaishou/"},
		{"Kwaipilot/KAT-Dev", "Kwaipilot/KAT-Dev", "kuaishou/"},
		{"Kwaipilot/KAT-Dev-72B-Exp", "Kwaipilot/KAT-Dev-72B-Exp", "kuaishou/"},

		// Black Forest Labs FLUX models - with prefix (already covered by bfl/)
		{"black-forest-labs/flux.1-pro", "black-forest-labs/flux.1-pro", "bfl/"},
		{"Black-Forest-Labs/FLUX.1-Pro", "Black-Forest-Labs/FLUX.1-Pro", "bfl/"},

		// Tencent Hunyuan models - should be tencent/
		{"tencent/hunyuan-mt-7b", "tencent/hunyuan-mt-7b", "tencent/"},
		{"Tencent/Hunyuan-MT-7B", "Tencent/Hunyuan-MT-7B", "tencent/"},

		// QVQ models (Alibaba Tongyi) - lowercase
		{"qvq-72b-preview", "qvq-72b-preview", "tongyi/"},
		{"qvq-72b", "qvq-72b", "tongyi/"},
		// QVQ models - mixed case
		{"QVQ-72B-Preview", "QVQ-72B-Preview", "tongyi/"},
		{"QVQ-72B", "QVQ-72B", "tongyi/"},
		// QVQ models - with prefix (qwen/ should become tongyi/)
		{"qwen/qvq-72b-preview", "qwen/qvq-72b-preview", "tongyi/"},
		{"Qwen/QVQ-72B-Preview", "Qwen/QVQ-72B-Preview", "tongyi/"},

		// QWQ models (Alibaba Tongyi reasoning) - already covered
		{"qwq-32b", "qwq-32b", "tongyi/"},
		{"QwQ-32B-Preview", "QwQ-32B-Preview", "tongyi/"},

		// ==================== OpenRouter Models ====================
		// AI21 models
		{"ai21/jamba-large-1.7", "ai21/jamba-large-1.7", "ai21/"},
		{"ai21/jamba-mini-1.7", "ai21/jamba-mini-1.7", "ai21/"},
		{"jamba-large-1.7", "jamba-large-1.7", "ai21/"},

		// Aion Labs models
		{"aion-labs/aion-1.0", "aion-labs/aion-1.0", "aion-labs/"},
		{"aion-labs/aion-1.0-mini", "aion-labs/aion-1.0-mini", "aion-labs/"},
		{"aion-1.0", "aion-1.0", "aion-labs/"},

		// Allen AI models
		{"allenai/olmo-2-0325-32b-instruct", "allenai/olmo-2-0325-32b-instruct", "allenai/"},
		{"allenai/olmo-3-32b-think", "allenai/olmo-3-32b-think", "allenai/"},
		{"allenai/molmo-2-8b:free", "allenai/molmo-2-8b:free", "allenai/"},
		{"olmo-3-7b-instruct", "olmo-3-7b-instruct", "allenai/"},
		{"molmo-2-8b", "molmo-2-8b", "allenai/"},

		// Amazon Nova models
		{"amazon/nova-2-lite-v1", "amazon/nova-2-lite-v1", "amazon/"},
		{"amazon/nova-premier-v1", "amazon/nova-premier-v1", "amazon/"},
		{"nova-pro-v1", "nova-pro-v1", "amazon/"},

		// Anthracite models
		{"anthracite-org/magnum-v4-72b", "anthracite-org/magnum-v4-72b", "anthracite/"},
		{"magnum-v4-72b", "magnum-v4-72b", "anthracite/"},

		// Arcee AI models (detection based on model name patterns)
		{"arcee-ai/coder-large", "arcee-ai/coder-large", ""},           // "coder-large" doesn't match arcee patterns
		{"arcee-ai/maestro-reasoning", "arcee-ai/maestro-reasoning", "arcee/"}, // "maestro" matches
		{"arcee-ai/trinity-mini", "arcee-ai/trinity-mini", "arcee/"},   // "trinity" matches
		{"arcee-ai/virtuoso-large", "arcee-ai/virtuoso-large", "arcee/"}, // "virtuoso" matches
		{"arcee-ai/spotlight", "arcee-ai/spotlight", "arcee/"},         // "spotlight" matches
		{"trinity-mini", "trinity-mini", "arcee/"},
		{"maestro-reasoning", "maestro-reasoning", "arcee/"},

		// Baidu ERNIE models
		{"baidu/ernie-4.5-21b-a3b", "baidu/ernie-4.5-21b-a3b", "baidu/"},
		{"baidu/ernie-4.5-300b-a47b", "baidu/ernie-4.5-300b-a47b", "baidu/"},
		{"ernie-4.5-21b-a3b", "ernie-4.5-21b-a3b", "baidu/"},

		// ByteDance Seed models (detection based on model name patterns)
		{"bytedance-seed/seed-1.6", "bytedance-seed/seed-1.6", "bytedance/"}, // "seed-1" matches seed-\d pattern
		{"bytedance-seed/seed-1.6-flash", "bytedance-seed/seed-1.6-flash", "bytedance/"},
		{"bytedance/ui-tars-1.5-7b", "bytedance/ui-tars-1.5-7b", ""},   // "ui-tars" doesn't match any pattern

		// Cohere models
		{"cohere/command-a", "cohere/command-a", "cohere/"},
		{"cohere/command-r-08-2024", "cohere/command-r-08-2024", "cohere/"},
		{"cohere/command-r7b-12-2024", "cohere/command-r7b-12-2024", "cohere/"},
		{"command-a", "command-a", "cohere/"},

		// DeepCogito models
		{"deepcogito/cogito-v2-preview-llama-109b-moe", "deepcogito/cogito-v2-preview-llama-109b-moe", "deepcogito/"},
		{"deepcogito/cogito-v2.1-671b", "deepcogito/cogito-v2.1-671b", "deepcogito/"},
		{"cogito-v2-preview-llama-70b", "cogito-v2-preview-llama-70b", "deepcogito/"},

		// DeepSeek models (OpenRouter format)
		{"deepseek/deepseek-chat", "deepseek/deepseek-chat", "deepseek/"},
		{"deepseek/deepseek-r1", "deepseek/deepseek-r1", "deepseek/"},
		{"deepseek/deepseek-r1-0528", "deepseek/deepseek-r1-0528", "deepseek/"},
		{"deepseek/deepseek-v3.2", "deepseek/deepseek-v3.2", "deepseek/"},

		// EleutherAI models (detection based on model name patterns)
		{"eleutherai/llemma_7b", "eleutherai/llemma_7b", "eleutherai/"}, // "llemma" matches
		{"llemma_7b", "llemma_7b", "eleutherai/"},                       // "llemma" matches

		// Essential AI models
		{"essentialai/rnj-1-instruct", "essentialai/rnj-1-instruct", "essentialai/"},
		{"rnj-1-instruct", "rnj-1-instruct", "essentialai/"},

		// Google models (OpenRouter format)
		{"google/gemini-2.5-flash", "google/gemini-2.5-flash", "google/"},
		{"google/gemini-3-pro-preview", "google/gemini-3-pro-preview", "google/"},
		{"google/gemma-3-27b-it", "google/gemma-3-27b-it", "google/"},
		{"google/gemma-3n-e4b-it", "google/gemma-3n-e4b-it", "google/"},

		// IBM Granite models
		{"ibm-granite/granite-4.0-h-micro", "ibm-granite/granite-4.0-h-micro", "ibm/"},
		{"granite-4.0-h-micro", "granite-4.0-h-micro", "ibm/"},

		// Inception models
		{"inception/mercury", "inception/mercury", "inception/"},
		{"inception/mercury-coder", "inception/mercury-coder", "inception/"},
		{"mercury", "mercury", "inception/"},
		{"mercury-coder", "mercury-coder", "inception/"},

		// Inflection AI models
		{"inflection/inflection-3-pi", "inflection/inflection-3-pi", "inflection/"},
		{"inflection/inflection-3-productivity", "inflection/inflection-3-productivity", "inflection/"},
		{"inflection-3-pi", "inflection-3-pi", "inflection/"},

		// Kuaishou/Kwaipilot models (detection based on model name patterns)
		{"kwaipilot/kat-coder-pro", "kwaipilot/kat-coder-pro", ""},     // "kat-coder-pro" doesn't match kat-dev or kat-v\d

		// Liquid AI models
		{"liquid/lfm-2.2-6b", "liquid/lfm-2.2-6b", "liquid/"},
		{"liquid/lfm2-8b-a1b", "liquid/lfm2-8b-a1b", "liquid/"},
		{"lfm-2.2-6b", "lfm-2.2-6b", "liquid/"},
		{"lfm2-8b-a1b", "lfm2-8b-a1b", "liquid/"},

		// Mancer models
		{"mancer/weaver", "mancer/weaver", "mancer/"},
		{"weaver", "weaver", "mancer/"},

		// Meituan models
		{"meituan/longcat-flash-chat", "meituan/longcat-flash-chat", "meituan/"},

		// Meta Llama models (OpenRouter format)
		{"meta-llama/llama-3.3-70b-instruct", "meta-llama/llama-3.3-70b-instruct", "meta/"},
		{"meta-llama/llama-4-maverick", "meta-llama/llama-4-maverick", "meta/"},
		{"meta-llama/llama-4-scout", "meta-llama/llama-4-scout", "meta/"},
		{"meta-llama/llama-guard-4-12b", "meta-llama/llama-guard-4-12b", "meta/"},

		// Microsoft models (detection based on model name patterns)
		{"microsoft/phi-4", "microsoft/phi-4", "microsoft/"},          // "phi-4" matches phi- pattern
		{"microsoft/wizardlm-2-8x22b", "microsoft/wizardlm-2-8x22b", "wizardlm/"}, // "wizardlm" matches wizardlm pattern

		// MiniMax models (OpenRouter format)
		{"minimax/minimax-01", "minimax/minimax-01", "minimax/"},
		{"minimax/minimax-m1", "minimax/minimax-m1", "minimax/"},
		{"minimax/minimax-m2", "minimax/minimax-m2", "minimax/"},

		// Mistral models (OpenRouter format)
		{"mistralai/codestral-2508", "mistralai/codestral-2508", "mistral/"},
		{"mistralai/devstral-2512", "mistralai/devstral-2512", "mistral/"},
		{"mistralai/ministral-8b", "mistralai/ministral-8b", "mistral/"},
		{"mistralai/mistral-large-2512", "mistralai/mistral-large-2512", "mistral/"},
		{"mistralai/pixtral-large-2411", "mistralai/pixtral-large-2411", "mistral/"},
		{"mistralai/voxtral-small-24b-2507", "mistralai/voxtral-small-24b-2507", "mistral/"},

		// Moonshot/Kimi models (OpenRouter format)
		{"moonshotai/kimi-dev-72b", "moonshotai/kimi-dev-72b", "kimi/"},
		{"moonshotai/kimi-k2", "moonshotai/kimi-k2", "kimi/"},
		{"moonshotai/kimi-k2-thinking", "moonshotai/kimi-k2-thinking", "kimi/"},

		// Morph AI models
		{"morph/morph-v3-fast", "morph/morph-v3-fast", "morph/"},
		{"morph/morph-v3-large", "morph/morph-v3-large", "morph/"},
		{"morph-v3-fast", "morph-v3-fast", "morph/"},

		// NeverSleep models
		{"neversleep/llama-3.1-lumimaid-8b", "neversleep/llama-3.1-lumimaid-8b", "neversleep/"},
		{"neversleep/noromaid-20b", "neversleep/noromaid-20b", "neversleep/"},
		{"noromaid-20b", "noromaid-20b", "neversleep/"},
		{"lumimaid-8b", "lumimaid-8b", "neversleep/"},

		// Nex-AGI models (detection based on model name patterns)
		{"nex-agi/deepseek-v3.1-nex-n1", "nex-agi/deepseek-v3.1-nex-n1", "deepseek/"}, // "deepseek" matches first

		// Nous Research models (OpenRouter format, detection based on model name patterns)
		{"nousresearch/hermes-3-llama-3.1-405b", "nousresearch/hermes-3-llama-3.1-405b", "nous/"}, // "hermes" matches
		{"nousresearch/hermes-4-405b", "nousresearch/hermes-4-405b", "nous/"},                     // "hermes" matches
		{"nousresearch/deephermes-3-mistral-24b-preview", "nousresearch/deephermes-3-mistral-24b-preview", "nous/"}, // "hermes" in deephermes matches

		// NVIDIA models (OpenRouter format, detection based on model name patterns)
		{"nvidia/llama-3.1-nemotron-70b-instruct", "nvidia/llama-3.1-nemotron-70b-instruct", "meta/"}, // "llama" matches meta first
		{"nvidia/nemotron-3-nano-30b-a3b", "nvidia/nemotron-3-nano-30b-a3b", "nvidia/"},               // "nemotron" matches nvidia
		{"nvidia/nemotron-nano-12b-v2-vl", "nvidia/nemotron-nano-12b-v2-vl", "nvidia/"},               // "nemotron" matches nvidia

		// OpenAI models (OpenRouter format)
		{"openai/gpt-4o", "openai/gpt-4o", "openai/"},
		{"openai/gpt-5", "openai/gpt-5", "openai/"},
		{"openai/gpt-5-codex", "openai/gpt-5-codex", "openai/"},
		{"openai/gpt-oss-120b", "openai/gpt-oss-120b", "openai/"},
		{"openai/o3", "openai/o3", "openai/"},
		{"openai/o4-mini", "openai/o4-mini", "openai/"},

		// OpenGVLab/InternVL models
		{"opengvlab/internvl3-78b", "opengvlab/internvl3-78b", "internlm/"},

		// Perplexity models (OpenRouter format)
		{"perplexity/sonar", "perplexity/sonar", "perplexity/"},
		{"perplexity/sonar-pro", "perplexity/sonar-pro", "perplexity/"},
		{"perplexity/sonar-reasoning-pro", "perplexity/sonar-reasoning-pro", "perplexity/"},

		// Prime Intellect models
		{"prime-intellect/intellect-3", "prime-intellect/intellect-3", "prime-intellect/"},
		{"intellect-3", "intellect-3", "prime-intellect/"},

		// Qwen models (OpenRouter format)
		{"qwen/qwen-2.5-72b-instruct", "qwen/qwen-2.5-72b-instruct", "tongyi/"},
		{"qwen/qwen3-235b-a22b", "qwen/qwen3-235b-a22b", "tongyi/"},
		{"qwen/qwen3-coder", "qwen/qwen3-coder", "tongyi/"},
		{"qwen/qwq-32b", "qwen/qwq-32b", "tongyi/"},

		// Raifle models
		{"raifle/sorcererlm-8x22b", "raifle/sorcererlm-8x22b", "raifle/"},
		{"sorcererlm-8x22b", "sorcererlm-8x22b", "raifle/"},

		// Relace models
		{"relace/relace-apply-3", "relace/relace-apply-3", "relace/"},
		{"relace/relace-search", "relace/relace-search", "relace/"},

		// Sao10k models
		{"sao10k/l3-euryale-70b", "sao10k/l3-euryale-70b", "sao10k/"},
		{"sao10k/l3.3-euryale-70b", "sao10k/l3.3-euryale-70b", "sao10k/"},
		{"euryale-70b", "euryale-70b", "sao10k/"},

		// StepFun models (OpenRouter format)
		{"stepfun-ai/step3", "stepfun-ai/step3", "stepfun/"},

		// SwitchPoint models (detection based on model name patterns)
		{"switchpoint/router", "switchpoint/router", ""},              // "router" doesn't match any pattern

		// Tencent models (OpenRouter format)
		{"tencent/hunyuan-a13b-instruct", "tencent/hunyuan-a13b-instruct", "tencent/"},

		// TheDrummer models
		{"thedrummer/cydonia-24b-v4.1", "thedrummer/cydonia-24b-v4.1", "thedrummer/"},
		{"thedrummer/rocinante-12b", "thedrummer/rocinante-12b", "thedrummer/"},
		{"thedrummer/skyfall-36b-v2", "thedrummer/skyfall-36b-v2", "thedrummer/"},
		{"thedrummer/unslopnemo-12b", "thedrummer/unslopnemo-12b", "thedrummer/"},
		{"cydonia-24b-v4.1", "cydonia-24b-v4.1", "thedrummer/"},
		{"rocinante-12b", "rocinante-12b", "thedrummer/"},

		// TNG Technology models (detection based on model name patterns)
		{"tngtech/deepseek-r1t-chimera", "tngtech/deepseek-r1t-chimera", "deepseek/"}, // "deepseek" matches first
		{"tngtech/deepseek-r1t2-chimera", "tngtech/deepseek-r1t2-chimera", "deepseek/"}, // "deepseek" matches first
		{"tngtech/tng-r1t-chimera", "tngtech/tng-r1t-chimera", "tng/"},                  // "chimera" matches tng

		// Undi95 models
		{"undi95/remm-slerp-l2-13b", "undi95/remm-slerp-l2-13b", "undi95/"},
		{"remm-slerp-l2-13b", "remm-slerp-l2-13b", "undi95/"},

		// xAI Grok models (OpenRouter format)
		{"x-ai/grok-3", "x-ai/grok-3", "xai/"},
		{"x-ai/grok-3-mini", "x-ai/grok-3-mini", "xai/"},
		{"x-ai/grok-4", "x-ai/grok-4", "xai/"},
		{"x-ai/grok-4-fast", "x-ai/grok-4-fast", "xai/"},
		{"x-ai/grok-4.1-fast", "x-ai/grok-4.1-fast", "xai/"},
		{"x-ai/grok-code-fast-1", "x-ai/grok-code-fast-1", "xai/"},

		// Xiaomi models (OpenRouter format)
		{"xiaomi/mimo-v2-flash", "xiaomi/mimo-v2-flash", "xiaomi/"},

		// Z.ai/Zhipu GLM models (OpenRouter format)
		{"z-ai/glm-4-32b", "z-ai/glm-4-32b", "glm/"},
		{"z-ai/glm-4.5", "z-ai/glm-4.5", "glm/"},
		{"z-ai/glm-4.6", "z-ai/glm-4.6", "glm/"},
		{"z-ai/glm-4.7", "z-ai/glm-4.7", "glm/"},

		// Alpindale models
		{"alpindale/goliath-120b", "alpindale/goliath-120b", "alpindale/"},
		{"goliath-120b", "goliath-120b", "alpindale/"},

		// Gryphe models
		{"gryphe/mythomax-l2-13b", "gryphe/mythomax-l2-13b", "gryphe/"},
		{"mythomax-l2-13b", "mythomax-l2-13b", "gryphe/"},

		// Cognitive Computations models
		{"cognitivecomputations/dolphin-mistral-24b-venice-edition:free", "cognitivecomputations/dolphin-mistral-24b-venice-edition:free", "cognitivecomputations/"},
		{"dolphin-mistral-24b", "dolphin-mistral-24b", "cognitivecomputations/"},

		// Alibaba Tongyi DeepResearch
		{"alibaba/tongyi-deepresearch-30b-a3b", "alibaba/tongyi-deepresearch-30b-a3b", "tongyi/"},
		{"tongyi-deepresearch-30b-a3b", "tongyi-deepresearch-30b-a3b", "tongyi/"},

		// Other models (no specific brand match)
		{"code-supernova-1-million", "code-supernova-1-million", ""},
		{"emoji-gen-v1", "emoji-gen-v1", ""},
		{"emoji-style-birthday", "emoji-style-birthday", ""},
		{"tstars2.0", "tstars2.0", ""},
		{"vision-model", "vision-model", ""},

		// ==================== Hosting Platform Models (prefix stripped, detect by model name) ====================
		// DeepInfra hosted models
		{"deepinfra/Qwen/QwQ-32B", "deepinfra/Qwen/QwQ-32B", "tongyi/"},
		{"deepinfra/Qwen/Qwen3-235B-A22B-Instruct-2507", "deepinfra/Qwen/Qwen3-235B-A22B-Instruct-2507", "tongyi/"},
		{"deepinfra/deepseek-ai/DeepSeek-V3.1", "deepinfra/deepseek-ai/DeepSeek-V3.1", "deepseek/"},
		{"deepinfra/meta-llama/Llama-3.3-70B-Instruct", "deepinfra/meta-llama/Llama-3.3-70B-Instruct", "meta/"},
		{"deepinfra/google/gemma-3-27b-it", "deepinfra/google/gemma-3-27b-it", "google/"},
		{"deepinfra/moonshotai/Kimi-K2-Instruct", "deepinfra/moonshotai/Kimi-K2-Instruct", "kimi/"},
		{"deepinfra/zai-org/GLM-4.5", "deepinfra/zai-org/GLM-4.5", "glm/"},

		// SiliconFlow hosted models
		{"siliconflow/BAAI/bge-m3", "siliconflow/BAAI/bge-m3", "baai/"},
		{"siliconflow/BAAI/bge-reranker-v2-m3", "siliconflow/BAAI/bge-reranker-v2-m3", "baai/"},
		{"siliconflow/Qwen/Qwen3-8B", "siliconflow/Qwen/Qwen3-8B", "tongyi/"},
		{"siliconflow/THUDM/GLM-4-9B-0414", "siliconflow/THUDM/GLM-4-9B-0414", "glm/"},
		{"siliconflow/deepseek-ai/DeepSeek-R1-0528-Qwen3-8B", "siliconflow/deepseek-ai/DeepSeek-R1-0528-Qwen3-8B", "deepseek/"},
		{"siliconflow/FunAudioLLM/SenseVoiceSmall", "siliconflow/FunAudioLLM/SenseVoiceSmall", "tongyi/"},
		{"siliconflow/Kwai-Kolors/Kolors", "siliconflow/Kwai-Kolors/Kolors", "kuaishou/"},
		{"siliconflow/netease-youdao/bce-embedding-base_v1", "siliconflow/netease-youdao/bce-embedding-base_v1", "youdao/"},
		{"siliconflow/internlm/internlm2_5-7b-chat", "siliconflow/internlm/internlm2_5-7b-chat", "internlm/"},

		// ModelScope hosted models
		{"modelscope/Qwen/Qwen3-235B-A22B-Instruct-2507", "modelscope/Qwen/Qwen3-235B-A22B-Instruct-2507", "tongyi/"},
		{"modelscope/deepseek-ai/DeepSeek-V3.1", "modelscope/deepseek-ai/DeepSeek-V3.1", "deepseek/"},
		{"modelscope/ZhipuAI/GLM-4.5", "modelscope/ZhipuAI/GLM-4.5", "glm/"},
		{"modelscope/moonshotai/Kimi-K2-Instruct", "modelscope/moonshotai/Kimi-K2-Instruct", "kimi/"},
		{"modelscope/MiniMax/MiniMax-M1-80k", "modelscope/MiniMax/MiniMax-M1-80k", "minimax/"},
		{"modelscope/Menlo/Jan-nano", "modelscope/Menlo/Jan-nano", "menlo/"},
		{"modelscope/opencompass/CompassJudger-1-32B-Instruct", "modelscope/opencompass/CompassJudger-1-32B-Instruct", "opencompass/"},
		{"modelscope/stepfun-ai/step3", "modelscope/stepfun-ai/step3", "stepfun/"},
		{"modelscope/PaddlePaddle/ERNIE-4.5-21B-A3B-PT", "modelscope/PaddlePaddle/ERNIE-4.5-21B-A3B-PT", "baidu/"},
		{"modelscope/mistralai/Ministral-8B-Instruct-2410", "modelscope/mistralai/Ministral-8B-Instruct-2410", "mistral/"},

		// Groq hosted models
		{"groq/meta-llama/llama-4-maverick-17b-128e-instruct", "groq/meta-llama/llama-4-maverick-17b-128e-instruct", "meta/"},
		{"groq/qwen/qwen3-32b", "groq/qwen/qwen3-32b", "tongyi/"},
		{"groq/openai/gpt-oss-120b", "groq/openai/gpt-oss-120b", "openai/"},
		{"groq/moonshotai/kimi-k2-instruct", "groq/moonshotai/kimi-k2-instruct", "kimi/"},
		{"groq/deepseek-r1-distill-llama-70b", "groq/deepseek-r1-distill-llama-70b", "deepseek/"},
		{"groq/gemma2-9b-it", "groq/gemma2-9b-it", "google/"},
		{"groq/llama-3.1-8b-instant", "groq/llama-3.1-8b-instant", "meta/"},
		{"groq/llama-3.3-70b-versatile", "groq/llama-3.3-70b-versatile", "meta/"},

		// Cerebras hosted models
		{"cerebras/llama-3.3-70b", "cerebras/llama-3.3-70b", "meta/"},
		{"cerebras/qwen-3-235b-a22b-instruct-2507", "cerebras/qwen-3-235b-a22b-instruct-2507", "tongyi/"},
		{"cerebras/gpt-oss-120b", "cerebras/gpt-oss-120b", "openai/"},

		// Bailian hosted models
		{"bailian/deepseek-r1", "bailian/deepseek-r1", "deepseek/"},
		{"bailian/deepseek-v3.1", "bailian/deepseek-v3.1", "deepseek/"},
		{"bailian/qwen-max", "bailian/qwen-max", "tongyi/"},
		{"bailian/qwen3-235b-a22b", "bailian/qwen3-235b-a22b", "tongyi/"},
		{"bailian/qvq-plus", "bailian/qvq-plus", "tongyi/"},

		// GeminiCLI hosted models
		{"geminicli/gemini-2.5-flash", "geminicli/gemini-2.5-flash", "google/"},
		{"geminicli/gemini-2.5-pro", "geminicli/gemini-2.5-pro", "google/"},

		// Highlight hosted models
		{"highlight/claude-sonnet-4-20250514", "highlight/claude-sonnet-4-20250514", "anthropic/"},
		{"highlight/gemini-2.5-flash", "highlight/gemini-2.5-flash", "google/"},
		{"highlight/gpt-5", "highlight/gpt-5", "openai/"},
		{"highlight/grok-3-latest", "highlight/grok-3-latest", "xai/"},
		{"highlight/o3", "highlight/o3", "openai/"},

		// Warp hosted models
		{"warp/claude-4-opus", "warp/claude-4-opus", "anthropic/"},
		{"warp/claude-4-sonnet", "warp/claude-4-sonnet", "anthropic/"},
		{"warp/gpt-5", "warp/gpt-5", "openai/"},
		{"warp/gemini-2.5-pro", "warp/gemini-2.5-pro", "google/"},
		{"warp/o3", "warp/o3", "openai/"},
		{"warp/o4-mini", "warp/o4-mini", "openai/"},

		// BigModel hosted models
		{"bigmodel/glm-4.5-flash", "bigmodel/glm-4.5-flash", "glm/"},
		{"bigmodel/cogview-3-flash", "bigmodel/cogview-3-flash", "glm/"},

		// AkashChat hosted models
		{"akashchat/DeepSeek-V3.1", "akashchat/DeepSeek-V3.1", "deepseek/"},
		{"akashchat/Meta-Llama-3-3-70B-Instruct", "akashchat/Meta-Llama-3-3-70B-Instruct", "meta/"},
		{"akashchat/Qwen3-235B-A22B-Instruct-2507-FP8", "akashchat/Qwen3-235B-A22B-Instruct-2507-FP8", "tongyi/"},

		// OpenRouter hosted models (with openrouter/ prefix)
		{"openrouter/deepseek/deepseek-r1", "openrouter/deepseek/deepseek-r1", "deepseek/"},
		{"openrouter/google/gemini-2.5-flash", "openrouter/google/gemini-2.5-flash", "google/"},
		{"openrouter/meta-llama/llama-3.3-70b-instruct", "openrouter/meta-llama/llama-3.3-70b-instruct", "meta/"},
		{"openrouter/qwen/qwen3-235b-a22b", "openrouter/qwen/qwen3-235b-a22b", "tongyi/"},
		{"openrouter/moonshotai/kimi-k2", "openrouter/moonshotai/kimi-k2", "kimi/"},
		{"openrouter/z-ai/glm-4.5-air", "openrouter/z-ai/glm-4.5-air", "glm/"},
		{"openrouter/tencent/hunyuan-a13b-instruct", "openrouter/tencent/hunyuan-a13b-instruct", "tencent/"},
		{"openrouter/cognitivecomputations/dolphin-mistral-24b-venice-edition", "openrouter/cognitivecomputations/dolphin-mistral-24b-venice-edition", "cognitivecomputations/"},
		{"openrouter/nousresearch/deephermes-3-llama-3-8b-preview", "openrouter/nousresearch/deephermes-3-llama-3-8b-preview", "nous/"},

		// ==================== Additional models from merged_models.txt ====================
		// AkashChat hosted models (additional)
		{"AkashChat/AkashGen", "AkashChat/AkashGen", ""},                                   // AkashGen is not a known brand
		{"AkashChat/Qwen3-Next-80B-A3B-Instruct", "AkashChat/Qwen3-Next-80B-A3B-Instruct", "tongyi/"},
		{"AkashChat/meta-llama-Llama-4-Maverick-17B-128E-Instruct-FP8", "AkashChat/meta-llama-Llama-4-Maverick-17B-128E-Instruct-FP8", "meta/"},
		{"AkashChat/openai-gpt-oss-120b", "AkashChat/openai-gpt-oss-120b", "openai/"},

		// Bailian hosted models (additional)
		{"Bailian/codeqwen1.5-7b-chat", "Bailian/codeqwen1.5-7b-chat", "tongyi/"},
		{"Bailian/qwen-coder-plus", "Bailian/qwen-coder-plus", "tongyi/"},
		{"Bailian/qwen-mt-plus", "Bailian/qwen-mt-plus", "tongyi/"},
		{"Bailian/qwen-tts-2025-05-22", "Bailian/qwen-tts-2025-05-22", "tongyi/"},
		{"Bailian/qwen-vl-max", "Bailian/qwen-vl-max", "tongyi/"},
		{"Bailian/qwen-vl-ocr", "Bailian/qwen-vl-ocr", "tongyi/"},
		{"Bailian/qvq-max-2025-05-15", "Bailian/qvq-max-2025-05-15", "tongyi/"},
		{"Bailian/qwen3-coder-480b-a35b-instruct", "Bailian/qwen3-coder-480b-a35b-instruct", "tongyi/"},

		// BigModel hosted models (additional)
		{"BigModel/cogvideox-flash", "BigModel/cogvideox-flash", "glm/"},
		{"BigModel/glm-z1-flash", "BigModel/glm-z1-flash", "glm/"},

		// Cerebras hosted models (additional)
		{"Cerebras/llama-4-maverick-17b-128e-instruct", "Cerebras/llama-4-maverick-17b-128e-instruct", "meta/"},
		{"Cerebras/llama-4-scout-17b-16e-instruct", "Cerebras/llama-4-scout-17b-16e-instruct", "meta/"},
		{"Cerebras/llama3.1-8b", "Cerebras/llama3.1-8b", "meta/"},
		{"Cerebras/qwen-3-coder-480b", "Cerebras/qwen-3-coder-480b", "tongyi/"},

		// DeepInfra hosted models (additional)
		{"Deepinfra/deepseek-ai/DeepSeek-Prover-V2-671B", "Deepinfra/deepseek-ai/DeepSeek-Prover-V2-671B", "deepseek/"},
		{"Deepinfra/meta-llama/Llama-4-Maverick-17B-128E-Instruct-FP8", "Deepinfra/meta-llama/Llama-4-Maverick-17B-128E-Instruct-FP8", "meta/"},
		{"Deepinfra/meta-llama/Llama-4-Scout-17B-16E-Instruct", "Deepinfra/meta-llama/Llama-4-Scout-17B-16E-Instruct", "meta/"},
		{"Deepinfra/meta-llama/Llama-Guard-4-12B", "Deepinfra/meta-llama/Llama-Guard-4-12B", "meta/"},
		{"Deepinfra/openai/gpt-oss-120b", "Deepinfra/openai/gpt-oss-120b", "openai/"},

		// DocsAnthropic prefix (documentation site)
		{"DocsAnthropic/claude-3-7-sonnet", "DocsAnthropic/claude-3-7-sonnet", "anthropic/"},

		// GeminiCLI hosted models (additional with Chinese path)
		{"GeminiCLI/假流式/gemini-2.5-flash", "GeminiCLI/假流式/gemini-2.5-flash", "google/"},
		{"GeminiCLI/流式抗截断/gemini-2.5-pro", "GeminiCLI/流式抗截断/gemini-2.5-pro", "google/"},

		// Google models (additional)
		{"Google/gemini-2.0-flash-thinking-exp", "Google/gemini-2.0-flash-thinking-exp", "google/"},
		{"Google/gemini-2.5-flash-image-preview", "Google/gemini-2.5-flash-image-preview", "google/"},
		{"Google/gemini-embedding-001", "Google/gemini-embedding-001", "google/"},
		{"Google/gemma-3-1b-it", "Google/gemma-3-1b-it", "google/"},
		{"Google/gemma-3n-e2b-it", "Google/gemma-3n-e2b-it", "google/"},

		// Grok prefix (xAI hosted)
		{"Grok/grok-3", "Grok/grok-3", "xai/"},
		{"Grok/grok-3-imageGen", "Grok/grok-3-imageGen", "xai/"},
		{"Grok/grok-3-search", "Grok/grok-3-search", "xai/"},
		{"Grok/grok-4", "Grok/grok-4", "xai/"},
		{"Grok/grok-4-deepsearch", "Grok/grok-4-deepsearch", "xai/"},
		{"Grok/grok-4-imageGen", "Grok/grok-4-imageGen", "xai/"},
		{"Grok/grok-4-reasoning", "Grok/grok-4-reasoning", "xai/"},

		// Groq hosted models (additional)
		{"Groq/allam-2-7b", "Groq/allam-2-7b", ""},                                         // allam is not a known brand
		{"Groq/compound-beta", "Groq/compound-beta", ""},                                   // compound is not a known brand
		{"Groq/compound-beta-mini", "Groq/compound-beta-mini", ""},                         // compound is not a known brand
		{"Groq/meta-llama/llama-guard-4-12b", "Groq/meta-llama/llama-guard-4-12b", "meta/"},
		{"Groq/meta-llama/llama-prompt-guard-2-22m", "Groq/meta-llama/llama-prompt-guard-2-22m", "meta/"},

		// Highlight hosted models (additional)
		{"Highlight/gpt-4.1", "Highlight/gpt-4.1", "openai/"},
		{"Highlight/gpt-5-chat", "Highlight/gpt-5-chat", "openai/"},
		{"Highlight/gpt-5-model-router", "Highlight/gpt-5-model-router", "openai/"},
		{"Highlight/grok-4-latest", "Highlight/grok-4-latest", "xai/"},
		{"Highlight/o3-mini", "Highlight/o3-mini", "openai/"},
		{"Highlight/us.anthropic.claude-3-7-sonnet-20250219-v1:0", "Highlight/us.anthropic.claude-3-7-sonnet-20250219-v1:0", "anthropic/"},

		// ModelScope hosted models (additional)
		{"Modelscope/LLM-Research/Llama-4-Maverick-17B-128E-Instruct", "Modelscope/LLM-Research/Llama-4-Maverick-17B-128E-Instruct", "meta/"},
		{"Modelscope/LLM-Research/Llama-4-Scout-17B-16E-Instruct", "Modelscope/LLM-Research/Llama-4-Scout-17B-16E-Instruct", "meta/"},
		{"Modelscope/LLM-Research/c4ai-command-r-plus-08-2024", "Modelscope/LLM-Research/c4ai-command-r-plus-08-2024", "cohere/"},
		{"Modelscope/MusePublic/Qwen-Image-Edit", "Modelscope/MusePublic/Qwen-Image-Edit", "tongyi/"},
		{"Modelscope/XGenerationLab/XiYanSQL-QwenCoder-32B-2412", "Modelscope/XGenerationLab/XiYanSQL-QwenCoder-32B-2412", "tongyi/"},
		{"Modelscope/wuhoutest5015/resume_ner", "Modelscope/wuhoutest5015/resume_ner", ""},  // resume_ner is not a known brand

		// OpenRouter hosted models (additional)
		{"Openrouter/agentica-org/deepcoder-14b-preview", "Openrouter/agentica-org/deepcoder-14b-preview", ""},  // deepcoder is not a known brand
		{"Openrouter/arliai/qwq-32b-arliai-rpr-v1", "Openrouter/arliai/qwq-32b-arliai-rpr-v1", "tongyi/"},
		{"Openrouter/microsoft/mai-ds-r1", "Openrouter/microsoft/mai-ds-r1", ""},            // mai-ds-r1 doesn't match phi- pattern
		{"Openrouter/rekaai/reka-flash-3", "Openrouter/rekaai/reka-flash-3", "reka/"},
		{"Openrouter/sarvamai/sarvam-m", "Openrouter/sarvamai/sarvam-m", ""},                // sarvam is not a known brand
		{"Openrouter/shisa-ai/shisa-v2-llama3.3-70b", "Openrouter/shisa-ai/shisa-v2-llama3.3-70b", "meta/"},

		// Qwen hosted models (additional)
		{"Qwen/qvq-72b-preview-0310", "Qwen/qvq-72b-preview-0310", "tongyi/"},
		{"Qwen/qwen-max-latest-image", "Qwen/qwen-max-latest-image", "tongyi/"},
		{"Qwen/qwen-max-latest-image-edit", "Qwen/qwen-max-latest-image-edit", "tongyi/"},
		{"Qwen/qwen-max-latest-search", "Qwen/qwen-max-latest-search", "tongyi/"},
		{"Qwen/qwen-max-latest-thinking", "Qwen/qwen-max-latest-thinking", "tongyi/"},
		{"Qwen/qwen-max-latest-video", "Qwen/qwen-max-latest-video", "tongyi/"},
		{"Qwen/qwen2.5-omni-7b", "Qwen/qwen2.5-omni-7b", "tongyi/"},
		{"Qwen/qwen3-coder-plus", "Qwen/qwen3-coder-plus", "tongyi/"},
		{"Qwen/qwen3-max-preview", "Qwen/qwen3-max-preview", "tongyi/"},

		// SiliconFlow hosted models (additional)
		{"Siliconflow/THUDM/GLM-4.1V-9B-Thinking", "Siliconflow/THUDM/GLM-4.1V-9B-Thinking", "glm/"},
		{"Siliconflow/THUDM/GLM-Z1-9B-0414", "Siliconflow/THUDM/GLM-Z1-9B-0414", "glm/"},

		// Warp hosted models (additional)
		{"Warp/claude-4-sonnet-auto", "Warp/claude-4-sonnet-auto", "anthropic/"},
		{"Warp/claude-4.1-opus", "Warp/claude-4.1-opus", "anthropic/"},
		{"Warp/gpt-4.1", "Warp/gpt-4.1", "openai/"},
		{"Warp/gpt-4o", "Warp/gpt-4o", "openai/"},
		{"Warp/warp-basic (lite)", "Warp/warp-basic (lite)", ""},                           // warp-basic is not a known brand

		// Z.ai/Zai hosted models
		{"Zai/GLM-4.5", "Zai/GLM-4.5", "glm/"},

		// Chinese prefix models
		{"非流美团/longcat-flash", "非流美团/longcat-flash", "meituan/"},

		// ==================== Additional models requested by user ====================
		// Nous Research models (additional)
		{"nous/hermes-4-70b", "nous/hermes-4-70b", "nous/"},
		{"nousresearch/hermes-4-70b-instruct", "nousresearch/hermes-4-70b-instruct", "nous/"},

		// OpenRouter models (additional)
		{"openrouter/bodybuilder", "openrouter/bodybuilder", ""},                           // bodybuilder is not a known brand

		// TNG models with :free suffix
		{"tng/tng-r1t-chimera:free", "tng/tng-r1t-chimera:free", "tng/"},
		{"tngtech/tng-r1t-chimera:free", "tngtech/tng-r1t-chimera:free", "tng/"},

		// ==================== No match cases ====================
		{"unknown-model", "unknown-model", ""},
		{"custom-model-v1", "custom-model-v1", ""},
		{"my-fine-tuned-model", "my-fine-tuned-model", ""},
		{"random-name-123", "random-name-123", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DetectBrandPrefix(tt.modelName)
			if got != tt.wantPrefix {
				t.Errorf("DetectBrandPrefix(%q) = %q, want %q", tt.modelName, got, tt.wantPrefix)
			}
		})
	}
}

func TestStripExistingPrefix(t *testing.T) {
	tests := []struct {
		name      string
		modelName string
		want      string
	}{
		{"no prefix", "gpt-4", "gpt-4"},
		{"with prefix", "openai/gpt-4", "gpt-4"},
		{"uppercase prefix", "ANT/gemini-3-pro", "gemini-3-pro"},
		{"mixed case prefix", "Google/gemini-pro", "gemini-pro"},
		{"multiple slashes", "vendor/sub/model", "sub/model"},
		{"trailing slash only", "model/", "model/"},
		{"leading slash only", "/model", "/model"},
		{"empty string", "", ""},
		{"just slash", "/", "/"},
		{"prefix only", "prefix/", "prefix/"},

		// Strip known non-brand prefixes (lora/, pro/, etc.)
		{"lora prefix", "lora/qwen/qwen2.5-32b", "qwen2.5-32b"},
		{"LORA prefix uppercase", "LORA/qwen/qwen2.5-32b", "qwen2.5-32b"},
		{"Lora prefix mixed", "Lora/Qwen/Qwen2.5-32B", "Qwen2.5-32B"},
		{"pro prefix", "pro/baai/bge-m3", "bge-m3"},
		{"PRO prefix uppercase", "PRO/baai/bge-m3", "bge-m3"},
		{"Pro prefix mixed", "Pro/BAAI/bge-m3", "bge-m3"},
		{"free prefix", "free/openai/gpt-4", "gpt-4"},
		{"fast prefix", "fast/deepseek/deepseek-chat", "deepseek-chat"},
		{"turbo prefix", "turbo/google/gemini-pro", "gemini-pro"},
		{"premium prefix", "premium/anthropic/claude-3", "claude-3"},
		{"basic prefix", "basic/meta/llama-3", "llama-3"},
		{"standard prefix", "standard/mistral/mistral-large", "mistral-large"},
		{"enterprise prefix", "enterprise/cohere/command-r", "command-r"},

		// Multiple non-brand prefixes (nested)
		{"lora+pro nested", "lora/pro/baai/bge-m3", "bge-m3"},
		{"pro+free nested", "pro/free/openai/gpt-4", "gpt-4"},

		// OpenRouter prefix (should be stripped to reveal actual model)
		{"openrouter prefix simple", "openrouter/meta-llama/llama-3.3-70b", "llama-3.3-70b"},
		{"openrouter prefix deepseek", "openrouter/deepseek/deepseek-r1", "deepseek-r1"},
		{"openrouter prefix google", "openrouter/google/gemini-2.5-flash", "gemini-2.5-flash"},
		{"openrouter prefix anthropic", "openrouter/anthropic/claude-3.5-sonnet", "claude-3.5-sonnet"},
		{"openrouter prefix qwen", "openrouter/qwen/qwen3-235b-a22b", "qwen3-235b-a22b"},
		{"openrouter prefix mistral", "openrouter/mistralai/mistral-large-2512", "mistral-large-2512"},
		{"OPENROUTER uppercase", "OPENROUTER/meta-llama/llama-3.3-70b", "llama-3.3-70b"},
		{"OpenRouter mixed case", "OpenRouter/DeepSeek/DeepSeek-R1", "DeepSeek-R1"},

		// DeepInfra prefix
		{"deepinfra prefix qwen", "deepinfra/Qwen/Qwen3-235B-A22B-Instruct-2507", "Qwen3-235B-A22B-Instruct-2507"},
		{"deepinfra prefix deepseek", "deepinfra/deepseek-ai/DeepSeek-V3.1", "DeepSeek-V3.1"},
		{"deepinfra prefix meta", "deepinfra/meta-llama/Llama-3.3-70B-Instruct", "Llama-3.3-70B-Instruct"},
		{"deepinfra prefix google", "deepinfra/google/gemma-3-27b-it", "gemma-3-27b-it"},

		// SiliconFlow prefix
		{"siliconflow prefix baai", "siliconflow/BAAI/bge-m3", "bge-m3"},
		{"siliconflow prefix qwen", "siliconflow/Qwen/Qwen3-8B", "Qwen3-8B"},
		{"siliconflow prefix thudm", "siliconflow/THUDM/GLM-4-9B-0414", "GLM-4-9B-0414"},
		{"siliconflow prefix deepseek", "siliconflow/deepseek-ai/DeepSeek-R1-0528-Qwen3-8B", "DeepSeek-R1-0528-Qwen3-8B"},

		// ModelScope prefix
		{"modelscope prefix qwen", "modelscope/Qwen/Qwen3-235B-A22B-Instruct-2507", "Qwen3-235B-A22B-Instruct-2507"},
		{"modelscope prefix deepseek", "modelscope/deepseek-ai/DeepSeek-V3.1", "DeepSeek-V3.1"},
		{"modelscope prefix zhipu", "modelscope/ZhipuAI/GLM-4.5", "GLM-4.5"},

		// Groq prefix
		{"groq prefix meta", "groq/meta-llama/llama-4-maverick-17b-128e-instruct", "llama-4-maverick-17b-128e-instruct"},
		{"groq prefix qwen", "groq/qwen/qwen3-32b", "qwen3-32b"},
		{"groq prefix openai", "groq/openai/gpt-oss-120b", "gpt-oss-120b"},

		// Cerebras prefix
		{"cerebras prefix llama", "cerebras/llama-3.3-70b", "llama-3.3-70b"},
		{"cerebras prefix qwen", "cerebras/qwen-3-235b-a22b-instruct-2507", "qwen-3-235b-a22b-instruct-2507"},

		// Bailian prefix
		{"bailian prefix deepseek", "bailian/deepseek-r1", "deepseek-r1"},
		{"bailian prefix qwen", "bailian/qwen-max", "qwen-max"},

		// Highlight prefix
		{"highlight prefix claude", "highlight/claude-sonnet-4-20250514", "claude-sonnet-4-20250514"},
		{"highlight prefix gemini", "highlight/gemini-2.5-flash", "gemini-2.5-flash"},
		{"highlight prefix gpt", "highlight/gpt-5", "gpt-5"},

		// Warp prefix
		{"warp prefix claude", "warp/claude-4-opus", "claude-4-opus"},
		{"warp prefix gpt", "warp/gpt-5", "gpt-5"},
		{"warp prefix o3", "warp/o3", "o3"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StripExistingPrefix(tt.modelName)
			if got != tt.want {
				t.Errorf("StripExistingPrefix(%q) = %q, want %q", tt.modelName, got, tt.want)
			}
		})
	}
}

func TestApplyBrandPrefix(t *testing.T) {
	tests := []struct {
		name         string
		modelName    string
		useLowercase bool
		want         string
	}{
		// Lowercase prefix tests
		{"deepseek lowercase", "deepseek-chat", true, "deepseek/deepseek-chat"},
		{"gpt lowercase", "gpt-4", true, "openai/gpt-4"},
		{"o3 lowercase", "o3", true, "openai/o3"},
		{"o3-pro lowercase", "o3-pro", true, "openai/o3-pro"},
		{"o4-mini lowercase", "o4-mini", true, "openai/o4-mini"},
		{"gemini lowercase", "gemini-pro", true, "google/gemini-pro"},
		{"claude lowercase", "claude-3-opus", true, "anthropic/claude-3-opus"},
		{"glm lowercase", "glm-4", true, "glm/glm-4"},
		{"kimi lowercase", "kimi-chat", true, "kimi/kimi-chat"},
		{"qwen lowercase", "qwen-turbo", true, "tongyi/qwen-turbo"},
		{"llama lowercase", "llama-3-70b", true, "meta/llama-3-70b"},
		{"mistral lowercase", "mistral-large", true, "mistral/mistral-large"},

		// Official brand name prefix tests (when lowercase is false)
		{"deepseek official", "deepseek-chat", false, "DeepSeek/deepseek-chat"},
		{"gpt official", "gpt-4", false, "OpenAI/gpt-4"},
		{"o3 official", "o3", false, "OpenAI/o3"},
		{"gemini official", "gemini-pro", false, "Google/gemini-pro"},
		{"claude official", "claude-3-opus", false, "Anthropic/claude-3-opus"},
		{"glm official", "glm-4", false, "GLM/glm-4"},
		{"kimi official", "kimi-chat", false, "Kimi/kimi-chat"},
		{"qwen official", "qwen-turbo", false, "Tongyi/qwen-turbo"},

		// Strip existing prefix and apply new one
		{"strip ANT prefix", "ANT/gemini-3-pro", true, "google/gemini-3-pro"},
		{"strip wrong prefix", "wrong/claude-3-opus", true, "anthropic/claude-3-opus"},
		{"strip and apply official", "old/gpt-4-turbo", false, "OpenAI/gpt-4-turbo"},
		{"strip old openai prefix", "OpenAI/o3-mini", true, "openai/o3-mini"},

		// No match - return stripped name for models without prefix
		{"unknown model lowercase", "unknown-model", true, "unknown-model"},
		{"unknown model capitalized", "unknown-model", false, "unknown-model"},

		// No match but has prefix - preserve original prefix
		{"prefixed unknown preserve", "vendor/unknown-model", true, "vendor/unknown-model"},
		{"prefixed unknown preserve official", "SomeVendor/custom-model", false, "SomeVendor/custom-model"},
		{"xiaomimimo preserve", "xiaomimimo/mimo-v2-flash", true, "xiaomi/mimo-v2-flash"},
		{"XiaomiMiMo preserve", "XiaomiMiMo/MiMo-V2-Flash", false, "Xiaomi/MiMo-V2-Flash"},

		// Xiaomi MiMo models
		{"mimo lowercase", "mimo-v2-flash", true, "xiaomi/mimo-v2-flash"},
		{"mimo official", "MiMo-V2-Flash", false, "Xiaomi/MiMo-V2-Flash"},

		// Meituan LongCat models
		{"longcat lowercase", "longcat-flash-chat", true, "meituan/longcat-flash-chat"},
		{"longcat official", "LongCat-Flash-Thinking", false, "Meituan/LongCat-Flash-Thinking"},

		// GLM normalization (glm4.7 -> glm-4.7)
		{"glm4.7 normalize lowercase", "glm4.7", true, "glm/glm-4.7"},
		{"glm4.7 normalize official", "GLM4.7", false, "GLM/GLM-4.7"},
		{"zhipu/glm-4.7 strip and normalize", "zhipu/glm-4.7", true, "glm/glm-4.7"},
		{"glm/glm4.7 normalize", "glm/glm4.7", true, "glm/glm-4.7"},
		{"z-ai/glm4.7 normalize", "z-ai/glm4.7", true, "glm/glm-4.7"},

		// ByteDance Seed models
		{"seed-oss lowercase", "seed-oss-36b-instruct", true, "bytedance/seed-oss-36b-instruct"},
		{"seed-oss official", "Seed-OSS-36B-Instruct", false, "ByteDance/Seed-OSS-36B-Instruct"},

		// FunAudioLLM/SenseVoice models (Tongyi)
		{"sensevoice lowercase", "sensevoicesmall", true, "tongyi/sensevoicesmall"},
		{"sensevoice official", "SenseVoiceSmall", false, "Tongyi/SenseVoiceSmall"},
		{"funaudiollm prefix strip", "funaudiollm/sensevoicesmall", true, "tongyi/sensevoicesmall"},

		// Kuaishou Kolors models
		{"kolors lowercase", "kolors", true, "kuaishou/kolors"},
		{"kolors official", "Kolors", false, "Kuaishou/Kolors"},
		{"kling lowercase", "kling-2.0", true, "kuaishou/kling-2.0"},
		{"kling official", "Kling-2.0", false, "Kuaishou/Kling-2.0"},

		// NetEase Youdao BCE models
		{"bce-embedding lowercase", "bce-embedding-base_v1", true, "youdao/bce-embedding-base_v1"},
		{"bce-embedding official", "BCE-Embedding-Base_v1", false, "Youdao/BCE-Embedding-Base_v1"},
		{"bce-reranker lowercase", "bce-reranker-base_v1", true, "youdao/bce-reranker-base_v1"},

		// THUDM models (-> GLM)
		{"thudm prefix strip", "thudm/glm-4-9b-chat", true, "glm/glm-4-9b-chat"},
		{"THUDM prefix strip official", "THUDM/GLM-4-9B-Chat", false, "GLM/GLM-4-9B-Chat"},

		// Strip lora/ prefix and apply brand
		{"lora prefix strip qwen", "lora/qwen/qwen2.5-32b-instruct", true, "tongyi/qwen2.5-32b-instruct"},
		{"lora prefix strip qwen official", "lora/Qwen/Qwen2.5-32B-Instruct", false, "Tongyi/Qwen2.5-32B-Instruct"},
		{"LORA prefix strip", "LORA/qwen/qwen2.5-32b-instruct", true, "tongyi/qwen2.5-32b-instruct"},

		// Strip pro/ prefix and apply brand
		{"pro prefix strip baai", "pro/baai/bge-m3", true, "baai/bge-m3"},
		{"pro prefix strip baai official", "Pro/BAAI/bge-m3", false, "BAAI/bge-m3"},
		{"pro prefix strip baai reranker", "pro/baai/bge-reranker-v2-m3", true, "baai/bge-reranker-v2-m3"},

		// Strip free/ prefix and apply brand
		{"free prefix strip openai", "free/openai/gpt-4", true, "openai/gpt-4"},
		{"free prefix strip anthropic", "free/anthropic/claude-3-opus", true, "anthropic/claude-3-opus"},

		// Strip fast/ prefix and apply brand
		{"fast prefix strip deepseek", "fast/deepseek/deepseek-chat", true, "deepseek/deepseek-chat"},
		{"fast prefix strip google", "fast/google/gemini-pro", true, "google/gemini-pro"},

		// Huawei Pangu models
		{"pangu lowercase", "pangu-pro-moe", true, "huawei/pangu-pro-moe"},
		{"pangu official", "Pangu-Pro-MoE", false, "Huawei/Pangu-Pro-MoE"},
		{"ascend-tribe prefix strip", "ascend-tribe/pangu-pro-moe", true, "huawei/pangu-pro-moe"},

		// China Telecom TeleAI models
		{"telespeech lowercase", "telespeechasr", true, "teleai/telespeechasr"},
		{"telespeech official", "TeleSpeechASR", false, "TeleAI/TeleSpeechASR"},
		{"teleai prefix strip", "teleai/telespeechasr", true, "teleai/telespeechasr"},

		// GPT-SoVITS models
		{"gpt-sovits lowercase", "gpt-sovits", true, "sovits/gpt-sovits"},
		{"gpt-sovits official", "GPT-SoVITS", false, "SoVITS/GPT-SoVITS"},
		{"rvc-boss prefix strip", "rvc-boss/gpt-sovits", true, "sovits/gpt-sovits"},

		// Kwaipilot/KAT models (Kuaishou)
		{"kat-dev lowercase", "kat-dev", true, "kuaishou/kat-dev"},
		{"kat-dev official", "KAT-Dev", false, "Kuaishou/KAT-Dev"},
		{"kwaipilot prefix strip", "kwaipilot/kat-dev", true, "kuaishou/kat-dev"},

		// Black Forest Labs FLUX models
		{"flux with bfl prefix", "black-forest-labs/flux.1-pro", true, "bfl/flux.1-pro"},
		{"flux with bfl prefix official", "Black-Forest-Labs/FLUX.1-Pro", false, "BFL/FLUX.1-Pro"},

		// Tencent Hunyuan models
		{"tencent hunyuan strip", "tencent/hunyuan-mt-7b", true, "tencent/hunyuan-mt-7b"},
		{"tencent hunyuan strip official", "Tencent/Hunyuan-MT-7B", false, "Tencent/Hunyuan-MT-7B"},

		// QVQ models (Alibaba Tongyi)
		{"qvq lowercase", "qvq-72b-preview", true, "tongyi/qvq-72b-preview"},
		{"qvq official", "QVQ-72B-Preview", false, "Tongyi/QVQ-72B-Preview"},
		{"qwen qvq prefix strip", "qwen/qvq-72b-preview", true, "tongyi/qvq-72b-preview"},

		// Edge cases
		{"empty string", "", true, ""},
		{"empty string capitalized", "", false, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ApplyBrandPrefix(tt.modelName, tt.useLowercase)
			if got != tt.want {
				t.Errorf("ApplyBrandPrefix(%q, %v) = %q, want %q", tt.modelName, tt.useLowercase, got, tt.want)
			}
		})
	}
}

func TestApplyBrandPrefixBatch(t *testing.T) {
	models := []string{
		"deepseek-chat",
		"gpt-4",
		"o3",
		"o3-pro",
		"gemini-pro",
		"claude-3-opus",
		"unknown-model",
	}

	// Test lowercase
	resultLower := ApplyBrandPrefixBatch(models, true)
	expectedLower := map[string]string{
		"deepseek-chat":  "deepseek/deepseek-chat",
		"gpt-4":          "openai/gpt-4",
		"o3":             "openai/o3",
		"o3-pro":         "openai/o3-pro",
		"gemini-pro":     "google/gemini-pro",
		"claude-3-opus":  "anthropic/claude-3-opus",
		"unknown-model":  "unknown-model",
	}

	for model, expected := range expectedLower {
		if resultLower[model] != expected {
			t.Errorf("ApplyBrandPrefixBatch lowercase: %q = %q, want %q", model, resultLower[model], expected)
		}
	}

	// Test capitalized (official brand names)
	resultUpper := ApplyBrandPrefixBatch(models, false)
	expectedUpper := map[string]string{
		"deepseek-chat":  "DeepSeek/deepseek-chat",
		"gpt-4":          "OpenAI/gpt-4",
		"o3":             "OpenAI/o3",
		"o3-pro":         "OpenAI/o3-pro",
		"gemini-pro":     "Google/gemini-pro",
		"claude-3-opus":  "Anthropic/claude-3-opus",
		"unknown-model":  "unknown-model",
	}

	for model, expected := range expectedUpper {
		if resultUpper[model] != expected {
			t.Errorf("ApplyBrandPrefixBatch capitalized: %q = %q, want %q", model, resultUpper[model], expected)
		}
	}
}

// TestDetectBrandPrefixCaseInsensitive verifies case-insensitive matching
func TestDetectBrandPrefixCaseInsensitive(t *testing.T) {
	tests := []struct {
		name       string
		modelName  string
		wantPrefix string
	}{
		{"lowercase deepseek", "deepseek-chat", "deepseek/"},
		{"uppercase DEEPSEEK", "DEEPSEEK-CHAT", "deepseek/"},
		{"mixed case DeepSeek", "DeepSeek-Chat", "deepseek/"},
		{"lowercase gpt", "gpt-4", "openai/"},
		{"uppercase GPT", "GPT-4", "openai/"},
		{"lowercase o3", "o3", "openai/"},
		{"uppercase O3", "O3", "openai/"},
		{"lowercase claude", "claude-3", "anthropic/"},
		{"uppercase CLAUDE", "CLAUDE-3", "anthropic/"},
		{"mixed case Claude", "Claude-3-Opus", "anthropic/"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DetectBrandPrefix(tt.modelName)
			if got != tt.wantPrefix {
				t.Errorf("DetectBrandPrefix(%q) = %q, want %q", tt.modelName, got, tt.wantPrefix)
			}
		})
	}
}

// TestApplyBrandPrefixWithSearchModels tests models with search suffix
func TestApplyBrandPrefixWithSearchModels(t *testing.T) {
	tests := []struct {
		name         string
		modelName    string
		useLowercase bool
		want         string
	}{
		{"deepseek-chat-search lowercase", "deepseek-chat-search", true, "deepseek/deepseek-chat-search"},
		{"deepseek-reasoner-search lowercase", "deepseek-reasoner-search", true, "deepseek/deepseek-reasoner-search"},
		{"gpt-4-search lowercase", "gpt-4-search", true, "openai/gpt-4-search"},
		{"gpt-4o-search-preview lowercase", "gpt-4o-search-preview", true, "openai/gpt-4o-search-preview"},
		{"claude-3-search lowercase", "claude-3-search", true, "anthropic/claude-3-search"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ApplyBrandPrefix(tt.modelName, tt.useLowercase)
			if got != tt.want {
				t.Errorf("ApplyBrandPrefix(%q, %v) = %q, want %q", tt.modelName, tt.useLowercase, got, tt.want)
			}
		})
	}
}

// TestOpenAIOSeriesModels specifically tests OpenAI o-series models
func TestOpenAIOSeriesModels(t *testing.T) {
	tests := []struct {
		modelName  string
		wantPrefix string
	}{
		// o1 series
		{"o1", "openai/"},
		{"o1-preview", "openai/"},
		{"o1-mini", "openai/"},
		{"o1-pro", "openai/"},
		// o3 series
		{"o3", "openai/"},
		{"o3-mini", "openai/"},
		{"o3-pro", "openai/"},
		{"o3-deep-research", "openai/"},
		// o4 series
		{"o4-mini", "openai/"},
		{"o4-mini-high", "openai/"},
		{"o4-mini-deep-research", "openai/"},
	}

	for _, tt := range tests {
		t.Run(tt.modelName, func(t *testing.T) {
			got := DetectBrandPrefix(tt.modelName)
			if got != tt.wantPrefix {
				t.Errorf("DetectBrandPrefix(%q) = %q, want %q", tt.modelName, got, tt.wantPrefix)
			}
		})
	}
}