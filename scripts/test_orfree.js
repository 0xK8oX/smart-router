async function parseEvents(stream) {
  const reader = stream.getReader();
  const decoder = new TextDecoder();
  let buffer = "";
  const events = [];
  while (true) {
    const { done, value } = await reader.read();
    if (value) buffer += decoder.decode(value, { stream: !done });
    let lastIndex = 0;
    for (let i = 0; i < buffer.length - 1; i++) {
      if (buffer[i] === "\n" && buffer[i + 1] === "\n") {
        events.push(buffer.slice(lastIndex, i + 2).trim());
        lastIndex = i + 2;
      }
    }
    buffer = buffer.slice(lastIndex);
    if (done) break;
  }
  if (buffer.trim()) events.push(buffer.trim());
  return events;
}

async function testAnthropic() {
  console.log("=== Anthropic format (/v1/messages) ===");
  const res = await fetch("http://localhost:23000/v1/messages", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      model: "auto",
      max_tokens: 10,
      messages: [{ role: "user", content: "Say hi" }],
      stream: true,
    }),
  });
  console.log(`Status: ${res.status}`);
  const events = await parseEvents(res.body);
  for (let i = 0; i < events.length; i++) {
    const lines = events[i].split("\n");
    const ev = lines.find(l => l.startsWith("event:"))?.slice(6).trim() || "(none)";
    const data = lines.find(l => l.startsWith("data:"))?.slice(5).trim() || "(none)";
    try {
      const parsed = JSON.parse(data);
      if (parsed.usage) {
        console.log(`[${i+1}] ${ev} *** USAGE: input=${parsed.usage.input_tokens}, output=${parsed.usage.output_tokens} ***`);
      } else if (ev === "message_start") {
        console.log(`[${i+1}] ${ev} id=${parsed.message?.id?.slice(0,20)}...`);
      } else {
        console.log(`[${i+1}] ${ev}`);
      }
    } catch {
      console.log(`[${i+1}] ${ev} ${data.slice(0,50)}`);
    }
  }
  console.log(`Total events: ${events.length}\n`);
}

async function testOpenAI() {
  console.log("=== OpenAI format (/v1/chat/completions) ===");
  const res = await fetch("http://localhost:23000/v1/chat/completions", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      model: "auto",
      max_tokens: 10,
      messages: [{ role: "user", content: "Say hi" }],
      stream: true,
    }),
  });
  console.log(`Status: ${res.status}`);
  const events = await parseEvents(res.body);
  for (let i = 0; i < events.length; i++) {
    const data = events[i].startsWith("data: ") ? events[i].slice(6) : events[i];
    if (data === "[DONE]") {
      console.log(`[${i+1}] [DONE]`);
      continue;
    }
    try {
      const parsed = JSON.parse(data);
      if (parsed.usage) {
        console.log(`[${i+1}] *** USAGE: prompt=${parsed.usage.prompt_tokens}, completion=${parsed.usage.completion_tokens} ***`);
      } else if (parsed.choices?.[0]?.delta?.content) {
        console.log(`[${i+1}] delta: "${parsed.choices[0].delta.content}"`);
      } else if (parsed.choices?.[0]?.finish_reason) {
        console.log(`[${i+1}] finish_reason: ${parsed.choices[0].finish_reason}`);
      } else {
        console.log(`[${i+1}] (other)`);
      }
    } catch {
      console.log(`[${i+1}] ${data.slice(0,50)}`);
    }
  }
  console.log(`Total events: ${events.length}\n`);
}

testAnthropic().then(() => testOpenAI());
