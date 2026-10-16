package llm

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go/bedrock"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// ProviderBedrock is Claude on Amazon Bedrock, through Bedrock's
// Messages-API endpoint (bedrock-mantle).
const ProviderBedrock = "bedrock"

// Bedrock provider settings. Credentials follow the AWS default chain
// (workload identity, instance role, AWS_* env, shared profile), or a
// Bedrock API key in AWS_BEARER_TOKEN_BEDROCK; the SDK resolves both.
const (
	// EnvBedrockRegion is the Bedrock region; AWS_REGION is the fallback.
	EnvBedrockRegion = "EXEMPLAR_BEDROCK_REGION"
	// EnvBedrockBaseURL overrides the regional endpoint (tests, VPC endpoints).
	EnvBedrockBaseURL = "EXEMPLAR_BEDROCK_BASE_URL"
	// EnvBedrockProfile selects a named AWS profile (local runs only).
	EnvBedrockProfile = "EXEMPLAR_BEDROCK_PROFILE"
)

var bedrockSpec = Spec{
	// Bedrock's Messages endpoint serves Claude models only, so every
	// route is the anthropic family: a Bedrock route never counts as an
	// independent verifier for a Claude generator.
	Family: func(string) string { return FamilyAnthropic },
	ValidateModel: func(model string) error {
		// Bedrock ids are anthropic.claude-*, optionally behind a
		// cross-region inference profile prefix (us., eu., global., ...).
		if !strings.HasPrefix(model, "anthropic.claude-") && !strings.Contains(model, ".anthropic.claude-") {
			return fmt.Errorf("%w: bedrock model ids look like anthropic.claude-* or <region>.anthropic.claude-*", errModelName)
		}
		return nil
	},
	New: func(ctx context.Context, s Settings) (Provider, error) {
		region := s.env(EnvBedrockRegion)
		if region == "" {
			region = s.env("AWS_REGION")
		}
		if region == "" {
			return nil, fmt.Errorf("%s or AWS_REGION %w", EnvBedrockRegion, errRequired)
		}
		cfg := bedrock.MantleClientConfig{
			AWSRegion:  region,
			BaseURL:    s.env(EnvBedrockBaseURL),
			AWSProfile: s.env(EnvBedrockProfile),
			// Static keys only when both are present; otherwise the SDK
			// uses AWS_BEARER_TOKEN_BEDROCK or the default credential chain.
			AWSAccessKey:       s.env("AWS_ACCESS_KEY_ID"),
			AWSSecretAccessKey: s.env("AWS_SECRET_ACCESS_KEY"),
			AWSSessionToken:    s.env("AWS_SESSION_TOKEN"),
		}
		if cfg.AWSAccessKey == "" || cfg.AWSSecretAccessKey == "" {
			cfg.AWSAccessKey, cfg.AWSSecretAccessKey, cfg.AWSSessionToken = "", "", ""
		}
		opts := []option.RequestOption{option.WithRequestTimeout(10 * time.Minute)}
		if s.HTTPClient != nil {
			opts = append(opts, option.WithHTTPClient(s.HTTPClient))
		}
		c, err := bedrock.NewMantleClient(ctx, cfg, opts...)
		if err != nil {
			return nil, fmt.Errorf("create bedrock client: %w", err)
		}
		return &bedrockProvider{client: c}, nil
	},
}

type bedrockProvider struct {
	client *bedrock.MantleClient
}

func (p *bedrockProvider) Name() string { return ProviderBedrock }

func (p *bedrockProvider) Send(ctx context.Context, req Request) (*Response, error) {
	return sendMessages(ctx, p.client.Messages, req)
}
