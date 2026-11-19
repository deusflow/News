/**
 * Danish News Bot - Breaking News Radar & Exact Schedule Dispatcher (Cloudflare Worker)
 * 
 * Этот скрипт бесплатно каждые 3 минуты:
 * 1) Проверяет датские RSS ленты и отправляет `breaking_news`, если находит срочную новость.
 * 2) Проверяет текущее время в Копенгагене (Europe/Copenhagen, с учетом CET/CEST) и
 *    отправляет `scheduled_news` в точные окна расписания (08:15, 10:20, 13:15, 17:25, 21:15),
 *    обходя многочасовые задержки встроенного cron планировщика GitHub Actions.
 */

const RSS_FEEDS = [
  "https://www.dr.dk/nyheder/service/feeds/senestenyt",
  "https://feeds.services.tv2.dk/api/feeds/nyheder/rss"
];

// Ключевые слова, которые триггерят "Молнию" в датских СМИ
const BREAKING_KEYWORDS = [
  "breaking", "urgent", "haster", "særlig vigtigt", 
  "lige nu", "pressemøde", "slår alarm", "evakueret", 
  "just in", "opdatering:", "ekstra:"
];

// Точное датское расписание (Europe/Copenhagen)
const SCHEDULED_SLOTS = [
  { hour: 8, minute: 15 },
  { hour: 10, minute: 20 },
  { hour: 13, minute: 15 },
  { hour: 17, minute: 25 },
  { hour: 21, minute: 15 }
];
const SCHEDULE_WINDOW_MINUTES = 12;

// Токен твоего GitHub теперь берется из безопасных секретов (env.GITHUB_PAT)
const GITHUB_OWNER = "deusflow"; // Твой username или организация (например open-gsd или deusflow)
const GITHUB_REPO = "News";

export default {
  // Вызывается по крону Cloudflare
  async scheduled(event, env, ctx) {
    ctx.waitUntil(Promise.all([
      checkFeeds(env),
      checkScheduledSlots(env)
    ]));
  },

  // Для ручного тестирования через браузер (защищено от внешнего спама без перенастройки секретов)
  async fetch(request, env, ctx) {
    const reqUrl = new URL(request.url);
    const tokenParam = reqUrl.searchParams.get("token") || "";
    const authHeader = (request.headers.get("Authorization") || "").replace(/^Bearer\s+/i, "").trim();
    const providedToken = tokenParam || authHeader;
    const expectedSecret = env.RADAR_SECRET || env.GITHUB_PAT || "";

    // Если кто-то просто открыл ссылку воркера без секретного ключа — не запускаем GitHub Actions
    if (!expectedSecret || providedToken !== expectedSecret) {
      return new Response("Danish News Radar active (cron mode).", { status: 200 });
    }

    const [foundBreaking, scheduledSlot] = await Promise.all([
      checkFeeds(env),
      checkScheduledSlots(env)
    ]);
    return new Response(
      `Radar check complete. Found breaking: ${foundBreaking}, scheduled slot: ${scheduledSlot || "none"}`
    );
  }
};

async function checkFeeds(env) {
  let foundBreaking = false;

  for (const feedUrl of RSS_FEEDS) {
    try {
      const response = await fetch(feedUrl, {
        headers: { "User-Agent": "Mozilla/5.0 (compatible; DK-NewsRadar/1.0)" }
      });
      
      if (!response.ok) {
        console.error(`${feedUrl} вернул ${response.status}`);
        continue;
      }
      
      const text = await response.text();

      // Улучшенный парсинг RSS <item> и <entry>
      const items = text.match(/<(item|entry)>([\s\S]*?)<\/(item|entry)>/gi) || [];

      for (const item of items) {
        // Извлекаем заголовок
        const titleMatch = item.match(/<title[^>]*><!\[CDATA\[(.*?)\]\]><\/title>/i) || 
                           item.match(/<title[^>]*>(.*?)<\/title>/i);
        
        // Извлекаем ссылку (через link, guid или href attribute)
        const linkMatch = item.match(/<link[^>]*href=["']([^"']+)["']/i) ||
                          item.match(/<link[^>]*><!\[CDATA\[(.*?)\]\]><\/link>/i) ||
                          item.match(/<link[^>]*>(.*?)<\/link>/i) ||
                          item.match(/<guid[^>]*>(https?:\/\/[^\s<]+)<\/guid>/i);

        if (!titleMatch || !linkMatch) continue;

        const title = titleMatch[1].replace(/<!\[CDATA\[(.*?)\]\]>/gi, "$1").trim();
        const link = linkMatch[1].replace(/<!\[CDATA\[(.*?)\]\]>/gi, "$1").trim();

        const titleLower = title.toLowerCase();
        const isBreaking = BREAKING_KEYWORDS.some(kw => titleLower.includes(kw));

        if (isBreaking) {
          // Проверяем, не отправляли ли мы уже эту новость
          // В Cloudflare Workers можно использовать KV для хранения состояния.
          // Если KV "DKNEWS_STATE" не привязан, пропускаем проверку дубликатов (только для тестов)
          if (env.DKNEWS_STATE) {
            const alreadySent = await env.DKNEWS_STATE.get(link);
            if (alreadySent) continue;
            // Помечаем как отправленное (живет 24 часа)
            await env.DKNEWS_STATE.put(link, "sent", { expirationTtl: 86400 });
          }

          console.log(`🚨 BREAKING FOUND: ${title}`);
          const dispatched = await dispatchGitHubEvent("breaking_news", { title, url: link }, env.GITHUB_PAT);
          if (dispatched) {
            foundBreaking = true;
          }
        }
      }
    } catch (err) {
      console.error(`Failed to fetch ${feedUrl}:`, err);
    }
  }

  return foundBreaking;
}

function getCopenhagenDateTime(date = new Date()) {
  const formatter = new Intl.DateTimeFormat("en-CA", {
    timeZone: "Europe/Copenhagen",
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    hourCycle: "h23"
  });

  const parts = {};
  for (const p of formatter.formatToParts(date)) {
    if (p.type !== "literal") {
      parts[p.type] = p.value;
    }
  }

  return {
    dateKey: `${parts.year}-${parts.month}-${parts.day}`,
    hour: Number(parts.hour),
    minute: Number(parts.minute)
  };
}

function pad2(num) {
  return String(num).padStart(2, "0");
}

async function checkScheduledSlots(env) {
  const { dateKey, hour, minute } = getCopenhagenDateTime();
  const nowMinutes = hour * 60 + minute;

  for (const slot of SCHEDULED_SLOTS) {
    const slotMinutes = slot.hour * 60 + slot.minute;
    const diff = nowMinutes - slotMinutes;
    const maxWindow = env.DKNEWS_STATE ? SCHEDULE_WINDOW_MINUTES : 3;

    if (diff < 0 || diff >= maxWindow) {
      continue;
    }

    const slotId = `${dateKey}-${pad2(slot.hour)}:${pad2(slot.minute)}`;
    const kvKey = `sched:${slotId}`;

    if (env.DKNEWS_STATE) {
      const alreadySent = await env.DKNEWS_STATE.get(kvKey);
      if (alreadySent) {
        continue;
      }
    } else {
      console.warn("DKNEWS_STATE KV binding missing; using narrow 3m schedule window.");
    }

    console.log(`⏰ SCHEDULED SLOT MATCHED (${slotId} Europe/Copenhagen)`);
    const ok = await dispatchGitHubEvent("scheduled_news", { slot: slotId }, env.GITHUB_PAT);
    if (ok) {
      if (env.DKNEWS_STATE) {
        await env.DKNEWS_STATE.put(kvKey, "sent", { expirationTtl: 86400 });
      }
      return slotId;
    }
  }

  return null;
}

async function dispatchGitHubEvent(eventType, clientPayload, pat) {
  if (!pat) {
    console.error("GITHUB_PAT secret is missing!");
    return false;
  }

  const dispatchUrl = `https://api.github.com/repos/${GITHUB_OWNER}/${GITHUB_REPO}/dispatches`;

  const response = await fetch(dispatchUrl, {
    method: "POST",
    headers: {
      "Accept": "application/vnd.github.v3+json",
      "Authorization": `Bearer ${pat}`,
      "Content-Type": "application/json",
      "User-Agent": "Cloudflare-Worker-Radar"
    },
    body: JSON.stringify({
      event_type: eventType,
      client_payload: clientPayload
    })
  });

  if (!response.ok) {
    console.error(`GitHub API Error (${eventType}):`, await response.text());
    return false;
  }

  console.log(`Successfully triggered GitHub Action (${eventType})!`);
  return true;
}
