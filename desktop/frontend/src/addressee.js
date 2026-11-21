// splitAddressee finds a request said to one agent by name: "코덱스한테 이거
// 고쳐 줘", "클로드야, …", "… 라고 코덱스한테 전해 줘". It returns {agent,
// text} with the name taken off, or null. A bare name is not enough —
// "코덱스 설정 열어 줘" talks about Codex — so it needs 한테/에게/께, a call
// (야/아) or a comma after the name. Same rules as the VS Code voice
// (aglink-voice extension/core/text.js).
const ADDRESSEES = [
  { agent: "codex", names: [String.raw`코덱스`, String.raw`코덱쓰`, String.raw`코댁스`, String.raw`codex`] },
  { agent: "claude", names: [String.raw`클로드\s*코드`, String.raw`클로드`, String.raw`클라우드\s*코드`, String.raw`claude\s*code`, String.raw`claude`] },
];
// after the name: 한테/에게/께 (maybe a comma), 야/아 then a comma or a space, or just a comma
const TO_RE = String.raw`(?:\s*(?:한테|에게|께)\s*[,，:]?|(?:야|아)\s*[,，]|(?:야|아)(?=\s)|\s*[,，:])`;
const ADDR_HEAD = ADDRESSEES.map((a) => ({ agent: a.agent, re: new RegExp(String.raw`^\s*(?:${a.names.join("|")})${TO_RE}\s*`, "i") }));
// "<request> (라고) 코덱스한테 전해/보내/말해/물어봐 (줘)" at the end
const ADDR_TAIL = ADDRESSEES.map((a) => ({
  agent: a.agent,
  re: new RegExp(
    String.raw`(?:\s+(?:이)?라고)?\s*[,，]?\s*(?:${a.names.join("|")})\s*(?:한테|에게|께)\s*(?:전해|보내|말해|물어\s*봐|물어|넣어|전달해|시켜)\s*(?:줘|주세요|줄래|봐|볼래)?\s*[.!?。]*\s*$`,
    "i"
  ),
}));

export function splitAddressee(text) {
  const t = String(text || "");
  for (const { agent, re } of ADDR_HEAD) {
    const m = re.exec(t);
    if (m && t.slice(m[0].length).trim()) return { agent, text: t.slice(m[0].length).trim() };
  }
  for (const { agent, re } of ADDR_TAIL) {
    const m = re.exec(t);
    if (m && t.slice(0, m.index).trim()) return { agent, text: t.slice(0, m.index).trim() };
  }
  return null;
}

export const AGENT_NAMES = { codex: "Codex", claude: "Claude" };
