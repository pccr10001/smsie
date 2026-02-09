package config

import (
	"log"
	"strings"

	"github.com/spf13/viper"
)

type Config struct {
	Server   ServerConfig   `mapstructure:"server"`
	Database DatabaseConfig `mapstructure:"database"`
	Serial   SerialConfig   `mapstructure:"serial"`
	Calling  CallingConfig  `mapstructure:"calling"`
	Webhook  WebhookConfig  `mapstructure:"webhook"`
	Users    UsersConfig    `mapstructure:"users"`
	Log      LogConfig      `mapstructure:"log"`
}

type LogConfig struct {
	Level string `mapstructure:"level"`
}

type ServerConfig struct {
	Port string `mapstructure:"port"`
	Mode string `mapstructure:"mode"`
}

type DatabaseConfig struct {
	Driver string `mapstructure:"driver"`
	DSN    string `mapstructure:"dsn"`
}

type SerialConfig struct {
	ScanInterval   string   `mapstructure:"scan_interval"`
	ExcludePorts   []string `mapstructure:"exclude_ports"`
	InitATCommands []string `mapstructure:"init_at_commands"`
}

type CallingConfig struct {
	STUNServers []string    `mapstructure:"stun_servers"`
	UDPPortMin  uint16      `mapstructure:"udp_port_min"`
	UDPPortMax  uint16      `mapstructure:"udp_port_max"`
	Audio       AudioConfig `mapstructure:"audio"`
}

type AudioConfig struct {
	DeviceKeyword    string `mapstructure:"device_keyword"`
	OutputDeviceName string `mapstructure:"output_device_name"`
	SampleRate       int    `mapstructure:"sample_rate"`
	Channels         int    `mapstructure:"channels"`
	BitsPerSample    int    `mapstructure:"bits_per_sample"`
	CaptureChunkMs   int    `mapstructure:"capture_chunk_ms"`
	PlaybackChunkMs  int    `mapstructure:"playback_chunk_ms"`
}

type WebhookConfig struct {
	TelegramToken  string `mapstructure:"telegram_token"`
	TelegramChatID string `mapstructure:"telegram_chat_id"`
	SlackURL       string `mapstructure:"slack_url"`
}

type UsersConfig struct {
	DefaultAdminPassword string `mapstructure:"default_admin_password"`
}

var AppConfig Config

func LoadConfig() {
	viper.SetConfigName("config")
	viper.SetConfigType("yaml")
	viper.AddConfigPath(".")

	viper.AutomaticEnv()
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))

	if err := viper.ReadInConfig(); err != nil {
		log.Printf("Warning: Config file not found, using defaults. Error: %v", err)
	}

	if err := viper.Unmarshal(&AppConfig); err != nil {
		log.Fatalf("Unable to decode into struct, %v", err)
	}

	if len(AppConfig.Calling.STUNServers) == 0 {
		AppConfig.Calling.STUNServers = []string{"stun:stun.l.google.com:19302"}
	}
	if AppConfig.Calling.Audio.DeviceKeyword == "" {
		AppConfig.Calling.Audio.DeviceKeyword = "AC Interface"
	}
	if AppConfig.Calling.Audio.SampleRate <= 0 {
		AppConfig.Calling.Audio.SampleRate = 8000
	}
	if AppConfig.Calling.Audio.Channels <= 0 {
		AppConfig.Calling.Audio.Channels = 1
	}
	if AppConfig.Calling.Audio.BitsPerSample <= 0 {
		AppConfig.Calling.Audio.BitsPerSample = 16
	}
	if AppConfig.Calling.Audio.CaptureChunkMs <= 0 {
		AppConfig.Calling.Audio.CaptureChunkMs = 40
	}
	if AppConfig.Calling.Audio.PlaybackChunkMs <= 0 {
		AppConfig.Calling.Audio.PlaybackChunkMs = 100
	}

	log.Println("Configuration loaded successfully")
}
