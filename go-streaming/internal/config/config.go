package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/joho/godotenv"
	"github.com/spf13/viper"
)

type ServerConfig struct {
	HTTPPort int    `mapstructure:"http_port" yaml:"http_port"`
	RTMPPort int    `mapstructure:"rtmp_port" yaml:"rtmp_port"`
	BaseURL  string `mapstructure:"base_url" yaml:"base_url"`
}

type R2Config struct {
	AccountID       string `mapstructure:"account_id" yaml:"account_id"`
	AccessKeyID     string `mapstructure:"access_key_id" yaml:"access_key_id"`
	SecretAccessKey string `mapstructure:"secret_access_key" yaml:"secret_access_key"`
	Endpoint        string `mapstructure:"endpoint" yaml:"endpoint"`
	RawBucket       string `mapstructure:"raw_bucket" yaml:"raw_bucket"`
	ProcessedBucket string `mapstructure:"processed_bucket" yaml:"processed_bucket"`
	PublicDomain    string `mapstructure:"public_domain" yaml:"public_domain"`
}

type StorageConfig struct {
	RootDir string   `mapstructure:"root_dir" yaml:"root_dir"`
	HLSDir  string   `mapstructure:"hls_dir" yaml:"hls_dir"`
	R2      R2Config `mapstructure:"r2" yaml:"r2"`
}

type TranscoderConfig struct {
	DockerImage        string `mapstructure:"docker_image" yaml:"docker_image"`
	GOPSize            int    `mapstructure:"gop_size" yaml:"gop_size"`
	HLSSegmentDuration int    `mapstructure:"hls_segment_duration" yaml:"hls_segment_duration"`
}

type DatabaseConfig struct {
	Host     string `mapstructure:"host" yaml:"host"`
	Port     int    `mapstructure:"port" yaml:"port"`
	User     string `mapstructure:"user" yaml:"user"`
	Password string `mapstructure:"password" yaml:"password"`
	DBName   string `mapstructure:"dbname" yaml:"dbname"`
	SSLMode  string `mapstructure:"sslmode" yaml:"sslmode"`
}

func (d DatabaseConfig) DSN() string {
	return fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
		d.Host, d.Port, d.User, d.Password, d.DBName, d.SSLMode)
}

type RabbitMQConfig struct {
	URI        string `mapstructure:"uri" yaml:"uri"`
	Exchange   string `mapstructure:"exchange" yaml:"exchange"`
	Queue      string `mapstructure:"queue" yaml:"queue"`
	RoutingKey string `mapstructure:"routing_key" yaml:"routing_key"`
	DLX        string `mapstructure:"dlx" yaml:"dlx"`
	DLQ        string `mapstructure:"dlq" yaml:"dlq"`
}

type Config struct {
	Server     ServerConfig     `mapstructure:"server" yaml:"server"`
	Storage    StorageConfig    `mapstructure:"storage" yaml:"storage"`
	Transcoder TranscoderConfig `mapstructure:"transcoder" yaml:"transcoder"`
	Database   DatabaseConfig   `mapstructure:"database" yaml:"database"`
	RabbitMQ   RabbitMQConfig   `mapstructure:"rabbitmq" yaml:"rabbitmq"`
}

// LoadConfig loads configuration from .env files, config.yaml, and environment variables
func LoadConfig(customPaths ...string) (*Config, error) {
	// 1. Try loading .env from multiple candidate locations
	_ = godotenv.Load(".env", "../.env", "../../.env")

	v := viper.New()
	v.SetConfigName("config")
	v.SetConfigType("yaml")

	v.AddConfigPath(".")
	v.AddConfigPath("..")
	v.AddConfigPath("../..")
	for _, p := range customPaths {
		if p != "" {
			v.AddConfigPath(p)
		}
	}

	v.SetEnvPrefix("BITMILL")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	// 2. Set Sensible Defaults
	v.SetDefault("server.http_port", 8080)
	v.SetDefault("server.rtmp_port", 1935)
	v.SetDefault("server.base_url", "http://localhost:8080")
	v.SetDefault("storage.root_dir", "..")
	v.SetDefault("storage.hls_dir", "../hls_storage")
	v.SetDefault("transcoder.docker_image", "mwader/static-ffmpeg:latest")
	v.SetDefault("transcoder.gop_size", 60)
	v.SetDefault("transcoder.hls_segment_duration", 2)

	v.SetDefault("database.host", "localhost")
	v.SetDefault("database.port", 5432)
	v.SetDefault("database.user", "bitmill_user")
	v.SetDefault("database.password", "bitmill_password")
	v.SetDefault("database.dbname", "bitmill_media")
	v.SetDefault("database.sslmode", "disable")

	v.SetDefault("rabbitmq.uri", "amqp://bitmill_mq:bitmill_mq_password@localhost:5672/")
	v.SetDefault("rabbitmq.exchange", "bitmill.video.events")
	v.SetDefault("rabbitmq.queue", "video.transcoding.jobs")
	v.SetDefault("rabbitmq.routing_key", "video.transcode.job")
	v.SetDefault("rabbitmq.dlx", "bitmill.video.dlx")
	v.SetDefault("rabbitmq.dlq", "video.transcoding.dlq")

	_ = v.ReadInConfig()

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal configuration: %w", err)
	}

	// 3. Fallback to standard environment variables if not present in config.yaml
	if val := os.Getenv("CLOUDFLARE_ACCOUNT_ID"); val != "" && cfg.Storage.R2.AccountID == "" {
		cfg.Storage.R2.AccountID = val
	}
	if val := os.Getenv("R2_ACCESS_KEY_ID"); val != "" && cfg.Storage.R2.AccessKeyID == "" {
		cfg.Storage.R2.AccessKeyID = val
	}
	if val := os.Getenv("R2_SECRET_ACCESS_KEY"); val != "" && cfg.Storage.R2.SecretAccessKey == "" {
		cfg.Storage.R2.SecretAccessKey = val
	}
	if val := os.Getenv("R2_RAW_BUCKET"); val != "" && cfg.Storage.R2.RawBucket == "" {
		cfg.Storage.R2.RawBucket = val
	}
	if val := os.Getenv("R2_PROCESSED_BUCKET"); val != "" && cfg.Storage.R2.ProcessedBucket == "" {
		cfg.Storage.R2.ProcessedBucket = val
	}
	if val := os.Getenv("R2_PUBLIC_DOMAIN"); val != "" && cfg.Storage.R2.PublicDomain == "" {
		cfg.Storage.R2.PublicDomain = val
	}
	if val := os.Getenv("R2_ENDPOINT"); val != "" && cfg.Storage.R2.Endpoint == "" {
		cfg.Storage.R2.Endpoint = val
	} else if cfg.Storage.R2.Endpoint == "" && cfg.Storage.R2.AccountID != "" {
		cfg.Storage.R2.Endpoint = fmt.Sprintf("https://%s.r2.cloudflarestorage.com", cfg.Storage.R2.AccountID)
	}

	return &cfg, nil
}
