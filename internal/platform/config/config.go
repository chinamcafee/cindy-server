package config

import (
	"fmt"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/joho/godotenv"
)

type Config struct {
	Env    string `env:"CINDY_ENV" envDefault:"dev"`
	Region string `env:"CINDY_REGION" envDefault:"global"`

	HTTPAddr      string `env:"CINDY_HTTP_ADDR" envDefault:":19828"`
	PublicBaseURL string `env:"CINDY_PUBLIC_BASE_URL" envDefault:"http://127.0.0.1:19828"`
	AdminBaseURL  string `env:"CINDY_ADMIN_BASE_URL" envDefault:"http://127.0.0.1:15900"`

	DatabaseURL string `env:"CINDY_DATABASE_URL" envDefault:"postgres://cindy:cindy_dev_password@127.0.0.1:5432/cindy?sslmode=disable"`
	RedisURL    string `env:"CINDY_REDIS_URL" envDefault:"redis://127.0.0.1:6379/0"`

	S3Endpoint      string `env:"CINDY_S3_ENDPOINT" envDefault:"http://127.0.0.1:9000"`
	S3AccessKey     string `env:"CINDY_S3_ACCESS_KEY" envDefault:"cindy"`
	S3SecretKey     string `env:"CINDY_S3_SECRET_KEY" envDefault:"cindy_dev_password"`
	S3BucketPublic  string `env:"CINDY_S3_BUCKET_PUBLIC" envDefault:"cindy-public"`
	S3BucketPrivate string `env:"CINDY_S3_BUCKET_PRIVATE" envDefault:"cindy-private"`
	S3BucketPlugins string `env:"CINDY_S3_BUCKET_PLUGINS" envDefault:"cindy-plugins"`
	S3BucketSkills  string `env:"CINDY_S3_BUCKET_SKILLS" envDefault:"cindy-skills"`
	S3UseSSL        bool   `env:"CINDY_S3_USE_SSL" envDefault:"false"`

	JWTPrivateKeyFile string        `env:"CINDY_JWT_PRIVATE_KEY_FILE" envDefault:"./.keys/jwt-ed25519.pem"`
	AccessTTL         time.Duration `env:"CINDY_ACCESS_TTL" envDefault:"15m"`
	RefreshTTL        time.Duration `env:"CINDY_REFRESH_TTL" envDefault:"720h"`
	MasterKey         string        `env:"CINDY_MASTER_KEY" envDefault:"cindy_dev_master_key_change_me"`

	SMTPAddr     string `env:"CINDY_SMTP_ADDR" envDefault:"127.0.0.1:1025"`
	SMTPFrom     string `env:"CINDY_SMTP_FROM" envDefault:"no-reply@cindy.local"`
	SMTPUsername string `env:"CINDY_SMTP_USERNAME"`
	SMTPPassword string `env:"CINDY_SMTP_PASSWORD"`
	SMTPTLS      string `env:"CINDY_SMTP_TLS" envDefault:"auto"`  // auto | starttls | ssl | none
	SMTPAuth     string `env:"CINDY_SMTP_AUTH" envDefault:"auto"` // auto | plain | login | none
	DevFixedCode      string `env:"CINDY_DEV_FIXED_CODE"`
	EmailDevFixedCode string `env:"CINDY_EMAIL_DEV_FIXED_CODE"`

	// Personal single-user mode attaches every verified email/phone identity to
	// one account instead of creating a new account per identity.
	SingleUserMode      bool   `env:"CINDY_SINGLE_USER_MODE" envDefault:"false"`
	SingleUserAccountID string `env:"CINDY_SINGLE_USER_ACCOUNT_ID"`

	SMSProvider              string `env:"CINDY_SMS_PROVIDER" envDefault:"auto"` // auto | log | aliyun
	AliyunSMSAccessKeyID     string `env:"CINDY_ALIYUN_SMS_ACCESS_KEY_ID"`
	AliyunSMSAccessKeySecret string `env:"CINDY_ALIYUN_SMS_ACCESS_KEY_SECRET"`
	AliyunSMSSignName        string `env:"CINDY_ALIYUN_SMS_SIGN_NAME"`
	AliyunSMSTemplateCode    string `env:"CINDY_ALIYUN_SMS_TEMPLATE_CODE"`
	AliyunSMSRegionID        string `env:"CINDY_ALIYUN_SMS_REGION_ID" envDefault:"cn-hangzhou"`
	AliyunSMSEndpoint        string `env:"CINDY_ALIYUN_SMS_ENDPOINT" envDefault:"https://dysmsapi.aliyuncs.com"`
	AliyunSMSTemplateParam   string `env:"CINDY_ALIYUN_SMS_TEMPLATE_PARAM" envDefault:"code"`

	TurnSecret string   `env:"CINDY_TURN_SECRET" envDefault:"cindy_turn_secret"`
	TurnURLs   []string `env:"CINDY_TURN_URLS" envSeparator:"," envDefault:"turn:127.0.0.1:3478?transport=udp"`

	AdminBootstrapEmail    string `env:"CINDY_ADMIN_BOOTSTRAP_EMAIL" envDefault:"admin@cindy.local"`
	AdminBootstrapPassword string `env:"CINDY_ADMIN_BOOTSTRAP_PASSWORD"`

	MetricsEnabled bool `env:"CINDY_METRICS_ENABLED" envDefault:"true"`

	// Static asset CDN and website stay outside the self-hosted API backend.
	AssetCDNBaseURL string `env:"CINDY_ASSET_CDN_BASE_URL"`
	WebsiteURL      string `env:"CINDY_WEBSITE_URL"`

	// Managed model inference gateway. Empty upstreams keep the gateway
	// disabled; keys are never written to logs or responses.
	GatewayAPIKey           string `env:"CINDY_GATEWAY_API_KEY"`
	GatewayOpenAIBaseURL    string `env:"CINDY_GATEWAY_OPENAI_BASE_URL"`
	GatewayOpenAIAPIKey     string `env:"CINDY_GATEWAY_OPENAI_API_KEY"`
	GatewayAnthropicBaseURL string `env:"CINDY_GATEWAY_ANTHROPIC_BASE_URL"`
	GatewayAnthropicAPIKey  string `env:"CINDY_GATEWAY_ANTHROPIC_API_KEY"`

	// Voice dictation data plane. Empty websocket URL disables voice sessions.
	VoiceASRWebSocketURL string `env:"CINDY_VOICE_ASR_WS_URL"`
	VoiceASRProvider     string `env:"CINDY_VOICE_ASR_PROVIDER" envDefault:"qwen3-asr-flash-realtime"`
	VoiceASRProtocol     string `env:"CINDY_VOICE_ASR_PROTOCOL_PROFILE" envDefault:"qwen-asr-server-vad"`
	VoiceASRSampleRate   int    `env:"CINDY_VOICE_ASR_SAMPLE_RATE" envDefault:"16000"`
	VoiceASRModel        string `env:"CINDY_VOICE_ASR_MODEL"`
	VoiceRefinerEnabled  bool   `env:"CINDY_VOICE_REFINER_ENABLED" envDefault:"false"`

	APNsKeyID          string `env:"CINDY_APNS_KEY_ID"`
	APNsTeamID         string `env:"CINDY_APNS_TEAM_ID"`
	APNsPrivateKeyFile string `env:"CINDY_APNS_PRIVATE_KEY_FILE"`
	APNsTopicGlobal    string `env:"CINDY_APNS_TOPIC_GLOBAL"`
	APNsTopicCN        string `env:"CINDY_APNS_TOPIC_CN"`

	AppleKeyFile  string `env:"CINDY_APPLE_KEY_FILE"`
	GoogleClient  string `env:"CINDY_GOOGLE_CLIENT_ID"`
	WeChatSecret  string `env:"CINDY_WECHAT_SECRET"`
	SlackSecret   string `env:"CINDY_SLACK_SECRET"`
	TelegramToken string `env:"CINDY_TELEGRAM_TOKEN"`
	GitHubToken   string `env:"CINDY_GITHUB_TOKEN"`
}

func Load() (*Config, error) {
	_ = godotenv.Load()
	cfg := &Config{}
	if err := env.Parse(cfg); err != nil {
		return nil, fmt.Errorf("parse env: %w", err)
	}
	if cfg.Region != "cn" && cfg.Region != "global" {
		return nil, fmt.Errorf("CINDY_REGION must be cn or global, got %q", cfg.Region)
	}
	if cfg.DatabaseURL == "" || cfg.RedisURL == "" {
		return nil, fmt.Errorf("database and redis URLs are required")
	}
	if cfg.Env == "prod" && (cfg.DevFixedCode != "" || cfg.EmailDevFixedCode != "") {
		return nil, fmt.Errorf("CINDY_DEV_FIXED_CODE and CINDY_EMAIL_DEV_FIXED_CODE must be empty in prod")
	}
	if cfg.AccessTTL <= 0 || cfg.RefreshTTL <= 0 {
		return nil, fmt.Errorf("token TTLs must be positive")
	}
	if cfg.SMSProvider != "auto" && cfg.SMSProvider != "log" && cfg.SMSProvider != "aliyun" {
		return nil, fmt.Errorf("CINDY_SMS_PROVIDER must be auto, log or aliyun")
	}
	switch cfg.SMTPTLS {
	case "auto", "starttls", "ssl", "none":
	default:
		return nil, fmt.Errorf("CINDY_SMTP_TLS must be auto, starttls, ssl or none")
	}
	switch cfg.SMTPAuth {
	case "auto", "plain", "login", "none":
	default:
		return nil, fmt.Errorf("CINDY_SMTP_AUTH must be auto, plain, login or none")
	}
	aliyunReady := cfg.AliyunSMSAccessKeyID != "" && cfg.AliyunSMSAccessKeySecret != "" &&
		cfg.AliyunSMSSignName != "" && cfg.AliyunSMSTemplateCode != ""
	if cfg.SMSProvider == "aliyun" && !aliyunReady {
		return nil, fmt.Errorf("CINDY_SMS_PROVIDER=aliyun requires access key, secret, sign name and template code")
	}
	if cfg.SMSProvider == "auto" {
		partial := (cfg.AliyunSMSAccessKeyID != "" || cfg.AliyunSMSAccessKeySecret != "" ||
			cfg.AliyunSMSSignName != "" || cfg.AliyunSMSTemplateCode != "") && !aliyunReady
		if partial {
			return nil, fmt.Errorf("Aliyun SMS configuration is incomplete")
		}
	}
	return cfg, nil
}

func (c *Config) AliyunSMSReady() bool {
	return c.AliyunSMSAccessKeyID != "" && c.AliyunSMSAccessKeySecret != "" &&
		c.AliyunSMSSignName != "" && c.AliyunSMSTemplateCode != ""
}

func (c *Config) IsDev() bool { return c.Env == "dev" }
