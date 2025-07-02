package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-co-op/gocron/v2"
)

type Config struct {
	TimeZone   string `json:"timezone"`
	Interface  string `json:"interface"`
	StatsFile  string `json:"stats_file"`
	WebhookURL string `json:"discord_webhook_url"`
	BotName    string `json:"bot_name"`
}

type Stats struct {
	Month string  `json:"month"`
	RX    big.Int `json:"rx"`
	TX    big.Int `json:"tx"`
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

func readNetworkBytes(interfaceName string) (big.Int, big.Int, error) {
	data, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return big.Int{}, big.Int{}, err
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
				return big.Int{}, big.Int{}, err
			}

			txBytes, err := strconv.ParseInt(parts[9], 10, 64)
			if err != nil {
				return big.Int{}, big.Int{}, err
			}

			var rxBig, txBig big.Int
			rxBig.SetInt64(rxBytes)
			txBig.SetInt64(txBytes)
			return rxBig, txBig, nil
		}
	}

	return big.Int{}, big.Int{}, fmt.Errorf("インターフェース %s が見つかりません", interfaceName)
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

func formatBytes(Bytes *big.Int) string {
	units := []string{"B", "KB", "MB", "GB", "TB"}
	fSize := new(big.Float).SetInt(Bytes)
	k := big.NewFloat(1024.0)
	for _, unit := range units {
		cmp := fSize.Cmp(k)
		if cmp == -1 {
			val, _ := fSize.Float64()
			return fmt.Sprintf("%.2f %s", val, unit)
		}
		fSize.Quo(fSize, k)
	}
	val, _ := fSize.Float64()
	return fmt.Sprintf("%.2f PB", val)
}

func sendToDiscord(interfaceName, rx, tx, total, webhookURL, botName string) error {
	month := time.Now().Format("2006年1月")
	embed := DiscordEmbed{
		Title:     fmt.Sprintf("%s の通信量（%s）", interfaceName, month),
		Color:     0x00bfff,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Fields: []EmbedField{
			{Name: "受信", Value: rx, Inline: true},
			{Name: "送信", Value: tx, Inline: true},
			{Name: "合計", Value: total, Inline: false},
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

func SendMonthlyNetStats() {
	config, err := readConfig("config.json")
	if err != nil {
		slog.Error("設定ファイルの読み込みエラー", "error", err)
		os.Exit(1)
	}

	monthKey := time.Now().Format("2006-01")

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

	usedRX := new(big.Int).Sub(&currentRX, &stats.RX)
	usedTX := new(big.Int).Sub(&currentTX, &stats.TX)

	if usedRX.Sign() < 0 || usedTX.Sign() < 0 {
		stats.RX = currentRX
		stats.TX = currentTX
		stats.Month = monthKey
		saveStats(config.StatsFile, stats)
		slog.Warn("カウントリセットを検出したため、今月の集計をリセットしました")
		return
	}

	total := new(big.Int).Add(usedRX, usedTX)

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

func main() {
	config, err := readConfig("config.json")
	if err != nil {
		slog.Error("設定ファイルの読み込みエラー", "error", err)
		os.Exit(1)
	}

	loc, _ := time.LoadLocation(config.TimeZone)
	s, err := gocron.NewScheduler(gocron.WithLocation(loc))
	if err != nil {
		slog.Error("スケジューラの作成に失敗", "error", err)
		os.Exit(1)
	}

	if _, err := os.Stat(config.StatsFile); os.IsNotExist(err) {
		SendMonthlyNetStats()
	}

	_, err = s.NewJob(
		gocron.CronJob("0 0 1 * *", false),
		gocron.NewTask(SendMonthlyNetStats),
	)
	if err != nil {
		slog.Error("ジョブの登録に失敗", "error", err)
		os.Exit(1)
	}

	s.Start()
	select {}
}
