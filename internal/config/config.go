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

	log.Println("Configuration loaded successfully")
}
