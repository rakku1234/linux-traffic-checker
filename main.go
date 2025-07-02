package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Interface  string `json:"interface"`
	StatsFile  string `json:"stats_file"`
	WebhookURL string `json:"discord_webhook_url"`
	BotName    string `json:"bot_name"`
}

type Stats struct {
	Month string `json:"month"`
	RX    int64  `json:"rx"`
	TX    int64  `json:"tx"`
}

type DiscordEmbed struct {
	Title     string       `json:"title"`
	Color     int          `json:"color"`
	Fields    []EmbedField `json:"fields"`
	Timestamp string       `json:"timestamp"`
}

type EmbedField struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline"`
}

type DiscordPayload struct {
	Username string         `json:"username"`
	Embeds   []DiscordEmbed `json:"embeds"`
}

func readConfig(filename string) (*Config, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}

	var config Config
	err = json.Unmarshal(data, &config)
	if err != nil {
		return nil, err
	}

	if strings.HasPrefix(config.StatsFile, "~") {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		config.StatsFile = strings.Replace(config.StatsFile, "~", homeDir, 1)
	}

	return &config, nil
}

func readNetworkBytes(interfaceName string) (int64, int64, error) {
	data, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return 0, 0, err
	}

	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		if strings.Contains(line, interfaceName) {
			parts := strings.Fields(line)
			if len(parts) < 10 {
				continue
			}

			rxBytes, err := strconv.ParseInt(parts[1], 10, 64)
			if err != nil {
				return 0, 0, err
			}

			txBytes, err := strconv.ParseInt(parts[9], 10, 64)
			if err != nil {
				return 0, 0, err
			}

			return rxBytes, txBytes, nil
		}
	}

	return 0, 0, fmt.Errorf("インターフェース %s が見つかりません", interfaceName)
}

func loadStats(statsFile string) (*Stats, bool, error) {
	if _, err := os.Stat(statsFile); os.IsNotExist(err) {
		return &Stats{}, true, nil
	}

	data, err := os.ReadFile(statsFile)
	if err != nil {
		return nil, false, err
	}

	var stats Stats
	err = json.Unmarshal(data, &stats)
	if err != nil {
		return nil, false, err
	}

	return &stats, false, nil
}

func saveStats(statsFile string, stats *Stats) error {
	data, err := json.Marshal(stats)
	if err != nil {
		return err
	}

	return os.WriteFile(statsFile, data, 0644)
}

func formatBytes(nBytes int64) string {
	units := []string{"B", "KB", "MB", "GB", "TB"}
	size := float64(nBytes)

	for _, unit := range units {
		if size < 1024.0 {
			return fmt.Sprintf("%.2f %s", size, unit)
		}
		size /= 1024.0
	}

	return fmt.Sprintf("%.2f PB", size)
}

func sendToDiscord(interfaceName, rxGB, txGB, totalGB, webhookURL, botName string) error {
	embed := DiscordEmbed{
		Title:     fmt.Sprintf("%s の通信量（今月）", interfaceName),
		Color:     0x00bfff,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Fields: []EmbedField{
			{Name: "受信", Value: rxGB, Inline: true},
			{Name: "送信", Value: txGB, Inline: true},
			{Name: "合計", Value: totalGB, Inline: false},
		},
	}

	payload := DiscordPayload{
		Username: botName,
		Embeds:   []DiscordEmbed{embed},
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	resp, err := http.Post(webhookURL, "application/json", strings.NewReader(string(jsonData)))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("discord API エラー: %s - %s", resp.Status, string(body))
	}

	return nil
}

func main() {
	config, err := readConfig("config.json")
	if err != nil {
		slog.Error("設定ファイルの読み込みエラー", "error", err)
		os.Exit(1)
	}

	today := time.Now()
	monthKey := today.Format("2006-01")

	stats, isFirstRun, err := loadStats(config.StatsFile)
	if err != nil {
		slog.Error("統計ファイルの読み込みエラー", "error", err)
		os.Exit(1)
	}

	currentRX, currentTX, err := readNetworkBytes(config.Interface)
	if err != nil {
		slog.Error("ネットワーク統計の読み込みエラー", "error", err)
		os.Exit(1)
	}

	if stats.Month != monthKey {
		stats = &Stats{
			Month: monthKey,
			RX:    currentRX,
			TX:    currentTX,
		}
		err = saveStats(config.StatsFile, stats)
		if err != nil {
			slog.Error("統計ファイルの保存エラー", "error", err)
			os.Exit(1)
		}
		slog.Info("新しい月の記録を開始しました")
	}

	if isFirstRun {
		slog.Info("初回起動のためDiscord通知をスキップします")
		return
	}

	usedRX := currentRX - stats.RX
	usedTX := currentTX - stats.TX
	total := usedRX + usedTX

	err = sendToDiscord(
		config.Interface,
		formatBytes(usedRX),
		formatBytes(usedTX),
		formatBytes(total),
		config.WebhookURL,
		config.BotName,
	)
	if err != nil {
		slog.Error("Discordへの送信エラー", "error", err)
		os.Exit(1)
	}
}
