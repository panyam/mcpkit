import { useState, useCallback } from "react";
import { useMCPApp, useMCPEvent } from "./useMCPApp";

function App() {
  const { connected, hostContext, callTool } = useMCPApp();
  const [serverTime, setServerTime] = useState("Loading...");
  const [messageText, setMessageText] = useState("This is message text.");
  const [logText, setLogText] = useState("This is log text.");
  const [linkUrl, setLinkUrl] = useState("https://modelcontextprotocol.io/");

  // Listen for tool results pushed by the host (LLM-initiated calls).
  useMCPEvent("toolresult", useCallback((data: MCPAppEventMap["toolresult"]) => {
    const result = data.result as any;
    const time = result?.structuredContent?.time || result?.content?.[0]?.text;
    if (time) setServerTime(time);
  }, []));

  const handleGetTime = async () => {
    try {
      const res = await callTool("get-time", {});
      const text = res.content?.[0]?.text;
      if (text) setServerTime(text);
    } catch (e) {
      setServerTime("[ERROR]");
      console.error(e);
    }
  };

  const handleSendMessage = async () => {
    try {
      await MCPApp.sendMessage({
        role: "user",
        content: [{ type: "text", text: messageText }],
      }, { timeout: 5000 });
    } catch (e) {
      console.error("Message send error:", e);
    }
  };

  const handleSendLog = () => {
    MCPApp.log("info", logText);
  };

  const handleOpenLink = () => {
    MCPApp.openLink(linkUrl);
  };

  const theme = hostContext?.theme || "unknown";

  return (
    <main style={{ padding: "1.5em", fontFamily: "system-ui" }}>
      <p style={{ fontSize: "0.85em", color: "#888" }}>
        {connected
          ? `Connected (${theme} mode) — watch activity in DevTools console`
          : "Connecting..."}
      </p>

      <Section title="Server Time">
        <p><strong>Time:</strong> <code>{serverTime}</code></p>
        <button onClick={handleGetTime} disabled={!connected}>
          Get Server Time
        </button>
      </Section>

      <Section title="Send Message">
        <textarea
          value={messageText}
          onChange={(e) => setMessageText(e.target.value)}
          rows={3}
          style={{ width: "100%", resize: "vertical" }}
        />
        <button onClick={handleSendMessage} disabled={!connected}>
          Send Message
        </button>
      </Section>

      <Section title="Send Log">
        <input
          type="text"
          value={logText}
          onChange={(e) => setLogText(e.target.value)}
          style={{ width: "100%" }}
        />
        <button onClick={handleSendLog} disabled={!connected}>
          Send Log
        </button>
      </Section>

      <Section title="Open Link">
        <input
          type="url"
          value={linkUrl}
          onChange={(e) => setLinkUrl(e.target.value)}
          style={{ width: "100%" }}
        />
        <button onClick={handleOpenLink} disabled={!connected}>
          Open Link
        </button>
      </Section>
    </main>
  );
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div style={{
      marginBottom: "1.5em",
      padding: "1em",
      border: "1px solid var(--color-border-primary, #ddd)",
      borderRadius: "8px",
    }}>
      <h3 style={{ margin: "0 0 0.5em" }}>{title}</h3>
      {children}
    </div>
  );
}

export default App;
