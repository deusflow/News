/**
 * Danish News Bot - Breaking News Radar (Cloudflare Worker)
 * 
 * Этот скрипт бесплатно проверяет датские RSS ленты каждые 3 минуты.
 * Если находит новость со словом "BREAKING", "URGENT" или "Særlig Vigtigt",
 * он отправляет вебхук (repository_dispatch) в твой GitHub, чтобы
 * запустить экстренный выпуск новостей.
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

// Токен твоего GitHub теперь берется из безопасных секретов (env.GITHUB_PAT)
const GITHUB_OWNER = "deusflow"; // Твой username или организация (например open-gsd или deusflow)
const GITHUB_REPO = "News";

export default {
  // Вызывается по крону Cloudflare
  async scheduled(event, env, ctx) {
    ctx.waitUntil(checkFeeds(env));
  },

  // Для ручного тестирования через браузер
  async fetch(request, env, ctx) {
    const result = await checkFeeds(env);
    return new Response(`Radar check complete. Found breaking: ${result}`);
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
                          item.match(/<link[^>]*><!\[CDATA\[(.*?)\]\]><\/title>/i) ||
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
          await triggerGitHubAction(title, link, env.GITHUB_PAT);
          foundBreaking = true;
        }
      }
    } catch (err) {
      console.error(`Failed to fetch ${feedUrl}:`, err);
    }
  }

  return foundBreaking;
}

async function triggerGitHubAction(title, url, pat) {
  if (!pat) {
    console.error("GITHUB_PAT secret is missing!");
    return;
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
      event_type: "breaking_news",
      client_payload: {
        title: title,
        url: url
      }
    })
  });

  if (!response.ok) {
    console.error("GitHub API Error:", await response.text());
  } else {
    console.log("Successfully triggered GitHub Action!");
  }
}
