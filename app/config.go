package app

import (
	"errors"
	"net"
	"strconv"
	"time"
)

type Config struct {
	Address            string
	ServerType         string
	MaxInFlight        int
	RequestTimeout     time.Duration
	MaxMessageBytes    int64         // 每条 WebSocket 消息在线上和正文解压后的字节上限，含 Pomelo 和消息头。默认 64 KiB。
	MaxPendingMessages int           // 每连接网络写队列条数，包含正在写入的一条；零使用默认值。
	MaxPendingBytes    int64         // 每连接网络写队列协议包字节数，包含正在写入的一条。
	WriteTimeout       time.Duration // 单次网络写入期限；零使用默认值。
}

func DefaultConfig() Config {
	return Config{
		Address:            "127.0.0.1:3250",
		ServerType:         "standalone",
		MaxInFlight:        128,
		RequestTimeout:     3 * time.Second,
		MaxMessageBytes:    64 * 1024,
		MaxPendingMessages: 32,
		MaxPendingBytes:    256 * 1024,
		WriteTimeout:       5 * time.Second,
	}
}

// prepare 在构造函数的配置副本上补齐网络默认值，保留调用方传入的配置。
func (cfg *Config) prepare(needsListener bool) error {
	if !needsListener && cfg.Address != "" {
		return errors.New("app: backend must not have a listener")
	}
	defaults := DefaultConfig()
	if cfg.MaxMessageBytes == 0 {
		cfg.MaxMessageBytes = defaults.MaxMessageBytes
	}
	if cfg.MaxPendingMessages == 0 {
		cfg.MaxPendingMessages = defaults.MaxPendingMessages
	}
	if cfg.MaxPendingBytes == 0 {
		cfg.MaxPendingBytes = defaults.MaxPendingBytes
	}
	if cfg.WriteTimeout == 0 {
		cfg.WriteTimeout = defaults.WriteTimeout
	}
	if cfg.MaxPendingMessages < 1 || cfg.MaxPendingBytes < 1 || cfg.WriteTimeout < 0 {
		return errors.New("app: invalid send limits")
	}
	if needsListener {
		_, portText, err := net.SplitHostPort(cfg.Address)
		port, portErr := strconv.Atoi(portText)
		if err != nil || portErr != nil || port < 0 || port > 65535 {
			return errors.New("app: invalid configuration")
		}
	}
	if !validName(cfg.ServerType) || cfg.MaxInFlight < 1 || cfg.RequestTimeout <= 0 || cfg.MaxMessageBytes < 1 {
		return errors.New("app: invalid configuration")
	}
	return nil
}
