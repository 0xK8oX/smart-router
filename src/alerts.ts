/**
 * Smart Router - Outage Alerts
 *
 * Fire-and-forget Telegram notifications when all providers in a plan fail.
 * Rate-limited via HealthTracker DO to avoid spam.
 */

const ALERT_COOLDOWN_MS = 15 * 60 * 1000; // 15 minutes between alerts for same plan

export interface AlertConfig {
  telegramBotToken?: string;
  telegramChatId?: string;
}

function getAlertConfig(env: Env): AlertConfig {
  const record = env as unknown as Record<string, string>;
  return {
    telegramBotToken: record.TELEGRAM_BOT_TOKEN,
    telegramChatId: record.TELEGRAM_CHAT_ID,
  };
}

export async function shouldSendAlert(
  env: Env,
  plan: string
): Promise<boolean> {
  const id = env.HEALTH_TRACKER.idFromName("global");
  const stub = env.HEALTH_TRACKER.get(id);
  try {
    const res = await stub.fetch("https://fake-host/health/shouldAlert", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ plan }),
    });
    if (!res.ok) return true; // fail open
    const data = (await res.json()) as { send: boolean };
    return data.send;
  } catch {
    return true; // fail open: send alert if DO is unreachable
  }
}

export async function sendOutageAlert(
  env: Env,
  plan: string,
  errors: Array<{ provider: string; status: number; message: string }>
): Promise<void> {
  const config = getAlertConfig(env);
  if (!config.telegramBotToken || !config.telegramChatId) {
    console.log(`[ALERT] Skip: no Telegram config`);
    return;
  }

  const now = new Date().toISOString();
  const errorLines = errors.map((e) => {
    const msg = e.message.length > 60 ? e.message.slice(0, 60) + "..." : e.message;
    return `  • ${e.provider}: ${e.status ? `HTTP ${e.status}` : "error"} — ${msg}`;
  });

  const text = [
    `⚠️ *SMART ROUTER OUTAGE*`,
    `*Plan:* \`${plan}\``,
    `*Time:* ${now}`,
    `*All providers failed:*`,
    ...errorLines,
    ``,
    `Check status: \`/v1/status\``,
  ].join("\n");

  const url = `https://api.telegram.org/bot${config.telegramBotToken}/sendMessage`;
  try {
    const res = await fetch(url, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        chat_id: config.telegramChatId,
        text,
        parse_mode: "Markdown",
      }),
    });
    if (!res.ok) {
      const body = await res.text();
      console.log(`[ALERT] Telegram failed: ${res.status} ${body}`);
    } else {
      console.log(`[ALERT] Telegram sent: plan=${plan}`);
    }
  } catch (err) {
    const msg = err instanceof Error ? err.message : String(err);
    console.log(`[ALERT] Telegram exception: ${msg}`);
  }
}
