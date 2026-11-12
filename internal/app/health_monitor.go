package app

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/deusflow/News/internal/config"
	"github.com/deusflow/News/internal/logger"
	"github.com/deusflow/News/internal/metrics"
	"github.com/deusflow/News/internal/rss"
	"github.com/deusflow/News/internal/telegram"
)

func getSentMessagesCount(m *metrics.Metrics) int {
	if m == nil {
		return 0
	}
	stats := m.GetStats()
	if v, ok := stats["telegram_messages_sent"].(int64); ok {
		return int(v)
	}
	if v, ok := stats["telegram_messages_sent"].(int); ok {
		return v
	}
	return 0
}

func monitorFeedHealth(stateStore StateStore, cfg *config.Config, statuses map[string]rss.FeedStatus) {
	if stateStore == nil || cfg == nil || len(statuses) == 0 {
		return
	}

	for name, st := range statuses {
		key := "feed_fails:" + name
		curVal, _ := stateStore.GetState(key)
		fails, _ := strconv.Atoi(curVal)

		if st.Err != nil || st.ItemCount == 0 {
			fails++
			_ = stateStore.SetState(key, strconv.Itoa(fails))
			if fails == 3 {
				errMsg := "0 news items fetched"
				if st.Err != nil {
					errMsg = st.Err.Error()
				}
				msg := fmt.Sprintf("⚠️ <b>Сторож RSS-лент:</b>\nДжерело <b>%s</b> не віддає новин 3 запуски поспіль!\nURL: %s\nПомилка: %s",
					name, st.Source.URL, errMsg)
				_ = telegram.SendAdminAlert(cfg.Telegram.Token, cfg.Telegram.AdminChatID, msg)
				logger.Warn("RSS feed failed 3 consecutive runs, admin alerted", "feed", name, "error", errMsg)
			}
		} else {
			if fails > 0 {
				_ = stateStore.SetState(key, "0")
				logger.Info("RSS feed recovered after failures", "feed", name, "previous_fails", fails)
			}
		}
	}
}

func monitorEmptyRuns(stateStore StateStore, cfg *config.Config, m *metrics.Metrics, beforeSent int) {
	if stateStore == nil || cfg == nil || m == nil {
		return
	}

	afterSent := getSentMessagesCount(m)
	publishedThisRun := afterSent - beforeSent

	key := "consecutive_empty_runs"
	curVal, _ := stateStore.GetState(key)
	emptyRuns, _ := strconv.Atoi(curVal)

	if publishedThisRun == 0 {
		emptyRuns++
		_ = stateStore.SetState(key, strconv.Itoa(emptyRuns))
		if emptyRuns == 3 {
			msg := "⚠️ <b>Сторож запусків:</b>\nБот завершив 3 планові запуски поспіль без жодної публікації новин!\nПеревірте RSS-стрічки або критерії релевантності."
			_ = telegram.SendAdminAlert(cfg.Telegram.Token, cfg.Telegram.AdminChatID, msg)
			logger.Warn("3 consecutive runs had 0 publications, admin alerted", "empty_runs", emptyRuns)
		}
	} else {
		if emptyRuns > 0 {
			_ = stateStore.SetState(key, "0")
			logger.Info("Consecutive empty runs reset after successful publication", "previous_empty_runs", emptyRuns)
		}
	}
}

// IsCopenhagenQuietHours checks if the current time in Europe/Copenhagen is during night hours (22:30 - 07:30).
// Regular news posts should never wake subscribers up in the middle of the night.
// Can be overridden by setting ALLOW_NIGHT_PUBLISH=true.
func IsCopenhagenQuietHours(t time.Time) bool {
	if os.Getenv("ALLOW_NIGHT_PUBLISH") == "true" {
		return false
	}
	loc, err := time.LoadLocation("Europe/Copenhagen")
	if err != nil {
		return false
	}
	nowDK := t.In(loc)
	hour := nowDK.Hour()
	minute := nowDK.Minute()

	// Quiet hours: 22:30 to 07:30 Copenhagen time
	return (hour == 22 && minute >= 30) || (hour > 22) || (hour < 7) || (hour == 7 && minute < 30)
}
