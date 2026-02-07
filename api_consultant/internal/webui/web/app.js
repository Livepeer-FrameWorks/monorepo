(function () {
  "use strict";

  const API_URL = document.querySelector('meta[name="skipper-api-url"]')?.content || "/admin/api";

  let conversations = [];
  let activeId = null;
  let messages = [];
  let isStreaming = false;
  let editingConvId = null;
  let abortController = null;

  // DOM refs (assigned in init)
  let $list, $messages, $input, $sendBtn, $scrollBtn, $sidebar, $overlay, $convTitle;

  // --- Auth gate ---

  var loginPromise = null;

  function showLogin() {
    if (loginPromise) return loginPromise;
    loginPromise = new Promise(function (resolve) {
      var overlay = document.createElement("div");
      overlay.className = "login-overlay";
      overlay.innerHTML =
        '<div class="login-card">' +
        "<h3>Skipper</h3>" +
        "<p>Enter your API key to continue.</p>" +
        '<input type="password" id="login-key" placeholder="API key" autofocus>' +
        '<button id="login-btn" class="btn btn-primary">Sign in</button>' +
        '<p id="login-error" class="login-error" style="display:none">Invalid key</p>' +
        "</div>";
      document.body.appendChild(overlay);

      var input = document.getElementById("login-key");
      var btn = document.getElementById("login-btn");
      var error = document.getElementById("login-error");

      function tryLogin() {
        var key = input.value.trim();
        if (!key) return;
        btn.disabled = true;
        fetch(API_URL + "/auth", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ key: key }),
        })
          .then(function (res) {
            if (res.ok) {
              overlay.remove();
              loginPromise = null;
              resolve();
            } else {
              error.style.display = "block";
              btn.disabled = false;
            }
          })
          .catch(function () {
            error.style.display = "block";
            btn.disabled = false;
          });
      }

      btn.onclick = tryLogin;
      input.onkeydown = function (e) {
        if (e.key === "Enter") tryLogin();
      };
      setTimeout(function () {
        input.focus();
      }, 50);
    });
    return loginPromise;
  }

  // --- API helpers ---

  function headers() {
    return { "Content-Type": "application/json" };
  }

  async function fetchConversations() {
    try {
      const res = await fetch(API_URL + "/conversations", { headers: headers() });
      if (res.status === 401) {
        await showLogin();
        return fetchConversations();
      }
      if (!res.ok) return [];
      return await res.json();
    } catch {
      return [];
    }
  }

  async function fetchConversation(id) {
    try {
      const res = await fetch(API_URL + "/conversations/" + id, {
        headers: headers(),
      });
      if (res.status === 401) {
        await showLogin();
        return fetchConversation(id);
      }
      if (!res.ok) return null;
      return await res.json();
    } catch {
      return null;
    }
  }

  // --- Markdown renderer ---

  function escapeHtml(s) {
    return s
      .replace(/&/g, "&amp;")
      .replace(/</g, "&lt;")
      .replace(/>/g, "&gt;")
      .replace(/"/g, "&quot;")
      .replace(/'/g, "&#039;");
  }

  var copyIdCounter = 0;

  function renderMarkdown(text) {
    var blocks = [];
    var working = text.replace(/```([\s\S]*?)```/g, function (_, code) {
      var i = blocks.length;
      var id = copyIdCounter++;
      blocks.push(
        '<div class="code-block-wrap"><pre id="code-' +
          id +
          '"><code>' +
          escapeHtml(code.trim()) +
          '</code></pre><button class="code-copy-btn" data-copy-target="code-' +
          id +
          '">Copy</button></div>'
      );
      return "__BLOCK_" + i + "__";
    });
    working = escapeHtml(working);
    // Headings (process most-specific first)
    working = working.replace(/(?:^|\n)#### (.+)/g, "\n<h6>$1</h6>");
    working = working.replace(/(?:^|\n)### (.+)/g, "\n<h5>$1</h5>");
    working = working.replace(/(?:^|\n)## (.+)/g, "\n<h4>$1</h4>");
    working = working.replace(/(?:^|\n)# (.+)/g, "\n<h3>$1</h3>");
    working = working.replace(/`([^`]+)`/g, "<code>$1</code>");
    working = working.replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>");
    working = working.replace(/\*([^*]+)\*/g, "<em>$1</em>");
    working = working.replace(
      /\[([^\]]+)\]\((https?:\/\/[^)]+)\)/g,
      '<a href="$2" target="_blank" rel="noreferrer">$1</a>'
    );
    // Unordered lists (consecutive lines starting with - )
    working = working.replace(/(?:^|\n)((?:- .+(?:\n|$))+)/g, function (_, listBlock) {
      var items = listBlock
        .split("\n")
        .filter(function (l) {
          return l.indexOf("- ") === 0;
        })
        .map(function (l) {
          return "<li>" + l.slice(2) + "</li>";
        })
        .join("");
      return '<ul class="msg-list">' + items + "</ul>";
    });
    // Ordered lists (consecutive lines starting with N. )
    working = working.replace(/(?:^|\n)((?:\d+\. .+(?:\n|$))+)/g, function (_, listBlock) {
      var items = listBlock
        .split("\n")
        .filter(function (l) {
          return /^\d+\. /.test(l);
        })
        .map(function (l) {
          return "<li>" + l.replace(/^\d+\. /, "") + "</li>";
        })
        .join("");
      return '<ol class="msg-list">' + items + "</ol>";
    });
    working = working.replace(/\n/g, "<br>");
    blocks.forEach(function (block, i) {
      working = working.replace("__BLOCK_" + i + "__", block);
    });
    return working;
  }

  // --- Message rendering ---

  var confidenceLabels = {
    verified: "Verified",
    sourced: "Sourced",
    best_guess: "Best guess",
    unknown: "Unknown",
  };

  var toolLabels = {
    search_knowledge: "Searching knowledge base",
    search_web: "Searching the web",
    diagnose_rebuffering: "Diagnosing rebuffering",
    diagnose_buffer_health: "Diagnosing buffer health",
    diagnose_packet_loss: "Diagnosing packet loss",
    diagnose_routing: "Analyzing viewer routing",
    get_stream_health_summary: "Checking stream health",
    get_anomaly_report: "Detecting anomalies",
    create_stream: "Creating stream",
    update_stream: "Updating stream",
    delete_stream: "Deleting stream",
    refresh_stream_key: "Refreshing stream key",
    create_clip: "Creating clip",
    delete_clip: "Deleting clip",
    start_dvr: "Starting DVR recording",
    stop_dvr: "Stopping DVR recording",
    create_vod_upload: "Uploading VOD",
    complete_vod_upload: "Completing upload",
    abort_vod_upload: "Aborting upload",
    delete_vod_asset: "Deleting VOD asset",
    check_topup: "Checking billing",
    topup_balance: "Processing top-up",
    resolve_playback_endpoint: "Resolving playback",
    update_billing_details: "Updating billing",
    get_payment_options: "Loading payment options",
    submit_payment: "Processing payment",
    introspect_schema: "Reading API schema",
    generate_query: "Generating query",
    execute_query: "Querying API",
    search_support_history: "Searching support history",
  };

  function toolLabel(name) {
    return toolLabels[name] || name.replace(/_/g, " ");
  }

  function renderMessage(msg) {
    var role = msg.role || "assistant";
    var isUser = role === "user";
    var el = document.createElement("div");
    el.className = "msg " + role;
    el.dataset.id = msg.id || "";

    var body = document.createElement("div");
    body.className = "msg-body";

    // Role label + badge
    var roleEl = document.createElement("div");
    roleEl.className = "msg-role";
    roleEl.textContent = isUser ? "You" : "Skipper";
    if (!isUser && msg.confidence) {
      var badge = document.createElement("span");
      badge.className = "badge " + (msg.confidence || "").replace("_", "-");
      badge.textContent = confidenceLabels[msg.confidence] || msg.confidence;
      roleEl.appendChild(badge);
    }
    body.appendChild(roleEl);

    // Timestamp
    if (msg.createdAt) {
      var timeSpan = document.createElement("span");
      timeSpan.className = "msg-time";
      timeSpan.textContent = formatTime(msg.createdAt);
      roleEl.appendChild(timeSpan);
    }

    // Best-guess warning
    if (!isUser && msg.confidence === "best_guess") {
      var warn = document.createElement("div");
      warn.className = "best-guess-warn";
      warn.textContent = "Best guess \u2014 verify with primary data before acting.";
      body.appendChild(warn);
    }

    // Unknown confidence notice
    if (!isUser && msg.confidence === "unknown") {
      var notice = document.createElement("div");
      notice.className = "unknown-notice";
      notice.textContent = "I could not validate a confident answer based on available data.";
      body.appendChild(notice);
    }

    // Content
    var content = document.createElement("div");
    content.className = "msg-content";
    if (!isUser && !msg.content) {
      content.innerHTML =
        '<div class="thinking-dots"><span></span><span></span><span></span></div>';
    } else {
      content.innerHTML = renderMarkdown(msg.content || "");
    }
    body.appendChild(content);

    // Tool status chips (live during streaming)
    if (!isUser && msg.tools && msg.tools.length > 0) {
      var toolWrap = document.createElement("div");
      toolWrap.className = "tool-status";
      msg.tools.forEach(function (t) {
        var chip = document.createElement("div");
        chip.className = "tool-chip " + (t.status || "running");
        chip.dataset.tool = t.name;
        if (t.status === "running") {
          var spinner = document.createElement("div");
          spinner.className = "tool-spinner";
          chip.appendChild(spinner);
        } else {
          var icon = document.createElement("span");
          icon.className = "tool-icon";
          icon.textContent = t.status === "errored" ? "\u2717" : "\u2713";
          chip.appendChild(icon);
        }
        var label = document.createElement("span");
        label.textContent = toolLabel(t.name);
        chip.appendChild(label);
        toolWrap.appendChild(chip);
      });
      body.appendChild(toolWrap);
    }

    // Citations
    if (!isUser && msg.citations && msg.citations.length > 0) {
      body.appendChild(renderCitationBlock("Citations", msg.citations));
    }
    if (!isUser && msg.externalLinks && msg.externalLinks.length > 0) {
      body.appendChild(renderCitationBlock("External sources", msg.externalLinks));
    }

    // Details
    if (!isUser && msg.details && msg.details.length > 0) {
      var details = document.createElement("details");
      details.className = "msg-details";
      var summary = document.createElement("summary");
      summary.className = "details-summary";
      summary.textContent = "Details";
      details.appendChild(summary);
      msg.details.forEach(function (d) {
        if (d.title) {
          var dt = document.createElement("div");
          dt.style.fontWeight = "600";
          dt.style.marginTop = "8px";
          dt.textContent = d.title;
          details.appendChild(dt);
        }
        var pre = document.createElement("pre");
        pre.className = "details-pre";
        pre.textContent =
          typeof d.payload === "string" ? d.payload : JSON.stringify(d.payload, null, 2);
        details.appendChild(pre);
      });
      body.appendChild(details);
    }

    el.appendChild(body);
    return el;
  }

  function renderCitationBlock(label, items) {
    var wrap = document.createElement("div");
    wrap.className = "msg-citations";
    var lbl = document.createElement("div");
    lbl.className = "citation-label";
    lbl.textContent = label;
    wrap.appendChild(lbl);
    var ul = document.createElement("ul");
    ul.className = "citation-list";
    items.forEach(function (c) {
      var li = document.createElement("li");
      var a = document.createElement("a");
      a.href = c.url;
      a.target = "_blank";
      a.rel = "noreferrer";
      a.textContent = c.label || c.url;
      li.appendChild(a);
      ul.appendChild(li);
    });
    wrap.appendChild(ul);
    return wrap;
  }

  // --- Conversation sidebar ---

  function renderSidebar() {
    $list.innerHTML = "";
    if (conversations.length === 0) {
      var empty = document.createElement("div");
      empty.className = "sidebar-empty";
      empty.textContent = "No conversations yet";
      $list.appendChild(empty);
      return;
    }
    conversations.forEach(function (c) {
      var item = document.createElement("div");
      item.className = "sidebar-item" + (c.ID === activeId ? " active" : "");

      if (editingConvId === c.ID) {
        var input = document.createElement("input");
        input.className = "sidebar-rename-input";
        input.type = "text";
        input.value = c.Title || "";
        input.onclick = function (e) {
          e.stopPropagation();
        };
        input.onkeydown = function (e) {
          if (e.key === "Enter") {
            var val = input.value.trim();
            if (val) renameConversation(c.ID, val);
          } else if (e.key === "Escape") {
            editingConvId = null;
            renderSidebar();
          }
        };
        item.appendChild(input);
        item.onclick = function () {
          loadConversation(c.ID);
        };
        $list.appendChild(item);
        setTimeout(function () {
          input.focus();
          input.select();
        }, 0);
        return;
      }

      var info = document.createElement("div");
      info.className = "sidebar-item-info";
      var title = document.createElement("span");
      title.className = "sidebar-item-title";
      title.textContent = c.Title || "Untitled chat";
      info.appendChild(title);

      var metaParts = [];
      var lastMsg = c.LastMessageAt;
      if (lastMsg && lastMsg.Valid && lastMsg.Time) {
        var rel = relativeTime(lastMsg.Time);
        if (rel) metaParts.push(rel);
      } else if (c.UpdatedAt) {
        var rel2 = relativeTime(c.UpdatedAt);
        if (rel2) metaParts.push(rel2);
      }
      if (c.MessageCount > 0) {
        metaParts.push(c.MessageCount + " msg" + (c.MessageCount !== 1 ? "s" : ""));
      }
      if (metaParts.length > 0) {
        var meta = document.createElement("span");
        meta.className = "sidebar-item-meta";
        meta.textContent = metaParts.join(" \u00B7 ");
        info.appendChild(meta);
      }
      item.appendChild(info);

      var renBtn = document.createElement("button");
      renBtn.className = "sidebar-rename";
      renBtn.textContent = "\u270E";
      renBtn.title = "Rename conversation";
      renBtn.onclick = function (e) {
        e.stopPropagation();
        editingConvId = c.ID;
        renderSidebar();
      };
      item.appendChild(renBtn);

      var delBtn = document.createElement("button");
      delBtn.className = "sidebar-delete";
      delBtn.textContent = "\u00D7";
      delBtn.title = "Delete conversation";
      delBtn.onclick = function (e) {
        e.stopPropagation();
        deleteConversation(c.ID);
      };
      item.appendChild(delBtn);

      item.onclick = function () {
        loadConversation(c.ID);
      };
      $list.appendChild(item);
    });
  }

  async function loadConversations() {
    conversations = (await fetchConversations()) || [];
    renderSidebar();
  }

  async function deleteConversation(id) {
    if (!confirm("Delete this conversation?")) return;
    try {
      var res = await fetch(API_URL + "/conversations/" + id, {
        method: "DELETE",
        headers: headers(),
      });
      if (!res.ok) return;
      if (activeId === id) {
        newChat();
      }
      await loadConversations();
    } catch (err) {
      console.error("Failed to delete conversation:", err);
    }
  }

  async function renameConversation(id, title) {
    try {
      var res = await fetch(API_URL + "/conversations/" + id, {
        method: "PATCH",
        headers: headers(),
        body: JSON.stringify({ title: title }),
      });
      if (!res.ok) return;
      for (var i = 0; i < conversations.length; i++) {
        if (conversations[i].ID === id) {
          conversations[i].Title = title;
          break;
        }
      }
      if (activeId === id) {
        $convTitle.textContent = title;
      }
      editingConvId = null;
      renderSidebar();
    } catch (err) {
      console.error("Failed to rename conversation:", err);
    }
  }

  async function loadConversation(id) {
    activeId = id;
    renderSidebar();
    var convo = await fetchConversation(id);
    if (!convo) return;
    messages = (convo.Messages || []).map(function (m) {
      var sources = parseSources(m.Sources);
      return {
        id: m.ID,
        role: m.Role,
        content: m.Content,
        confidence: m.Confidence || undefined,
        citations: sources.citations,
        externalLinks: sources.externalLinks,
        details: tryParseDetails(m.ToolsUsed),
        createdAt: m.CreatedAt,
      };
    });
    renderMessages();
    $convTitle.textContent = convo.Title || "Chat";
    closeSidebar();
  }

  function parseSources(raw) {
    var empty = { citations: undefined, externalLinks: undefined };
    if (!raw) return empty;
    try {
      var arr = typeof raw === "string" ? JSON.parse(raw) : raw;
      if (!Array.isArray(arr)) return empty;
      var citations = [];
      var externalLinks = [];
      arr.forEach(function (s) {
        var url = s.URL || s.url;
        if (!url) return;
        var item = { label: s.Title || s.title || s.Label || s.label || "", url: url };
        if (s.Type === "web" || s.type === "web") {
          externalLinks.push(item);
        } else {
          citations.push(item);
        }
      });
      return {
        citations: citations.length > 0 ? citations : undefined,
        externalLinks: externalLinks.length > 0 ? externalLinks : undefined,
      };
    } catch {
      return empty;
    }
  }

  function tryParseDetails(raw) {
    if (!raw) return undefined;
    try {
      var parsed = typeof raw === "string" ? JSON.parse(raw) : raw;
      // New wrapped format: { calls: [...], details: [...] }
      if (parsed && !Array.isArray(parsed)) {
        if (Array.isArray(parsed.details) && parsed.details.length > 0) {
          return parsed.details;
        }
        if (Array.isArray(parsed.calls) && parsed.calls.length > 0) {
          return parsed.calls.map(function (d) {
            return { title: d.Name || d.name || "", payload: d.Arguments || d.arguments || d };
          });
        }
        return undefined;
      }
      // Old flat-array format
      if (!Array.isArray(parsed) || parsed.length === 0) return undefined;
      return parsed.map(function (d) {
        return { title: d.Name || d.name || "", payload: d.Arguments || d.arguments || d };
      });
    } catch {
      return undefined;
    }
  }

  function newChat() {
    activeId = null;
    messages = [];
    renderMessages();
    renderSidebar();
    $convTitle.textContent = "New chat";
    $input.focus();
    closeSidebar();
  }

  // --- Message display ---

  function renderMessages() {
    $messages.innerHTML = "";
    if (messages.length === 0) {
      var empty = document.createElement("div");
      empty.className = "messages-empty";
      empty.innerHTML =
        '<div class="empty-card"><h2>Skipper</h2><p>AI Video Consultant. Ask about stream health, viewer trends, configuration, or platform guidance.</p></div>';
      $messages.appendChild(empty);

      var prompts = [
        {
          icon: "\uD83D\uDC93",
          label: "Diagnostics",
          prompt: "Why are my viewers rebuffering?",
          desc: "Rebuffering, latency, packet loss",
        },
        {
          icon: "\uD83D\uDCE1",
          label: "Streams",
          prompt: "Show me my active streams",
          desc: "Create, manage, refresh keys",
        },
        {
          icon: "\uD83D\uDCCA",
          label: "Analytics",
          prompt: "Show me my stream health summary",
          desc: "Health summaries, anomalies",
        },
        {
          icon: "\uD83C\uDFAC",
          label: "Media",
          prompt: "Create a clip from my stream",
          desc: "Clips, DVR, VOD management",
        },
        {
          icon: "\uD83D\uDCD6",
          label: "Knowledge",
          prompt: "How do I set up SRT ingest?",
          desc: "Docs, guides, web search",
        },
        {
          icon: "\uD83D\uDCAC",
          label: "Support",
          prompt: "Search my past support tickets",
          desc: "Ticket history and context",
        },
      ];
      var grid = document.createElement("div");
      grid.className = "suggested-prompts";
      prompts.forEach(function (p) {
        var btn = document.createElement("button");
        btn.className = "suggested-btn";
        btn.onclick = function () {
          sendMessage(p.prompt);
        };
        var iconEl = document.createElement("span");
        iconEl.className = "suggested-icon";
        iconEl.textContent = p.icon;
        var lbl = document.createElement("span");
        lbl.className = "suggested-label";
        lbl.textContent = p.label;
        var txt = document.createElement("span");
        txt.className = "suggested-text";
        txt.textContent = p.prompt;
        var desc = document.createElement("span");
        desc.className = "suggested-desc";
        desc.textContent = p.desc;
        btn.appendChild(iconEl);
        btn.appendChild(lbl);
        btn.appendChild(txt);
        btn.appendChild(desc);
        grid.appendChild(btn);
      });
      $messages.appendChild(grid);
      var disclaimer = document.createElement("p");
      disclaimer.className = "suggested-disclaimer";
      disclaimer.textContent =
        "Skipper can also manage DVR recordings, check billing, explore the GraphQL API, and more \u2014 just ask.";
      $messages.appendChild(disclaimer);
      return;
    }
    messages.forEach(function (m) {
      $messages.appendChild(renderMessage(m));
    });
    scrollToBottom();
  }

  function updateLastAssistant(update) {
    for (var i = messages.length - 1; i >= 0; i--) {
      if (messages[i].role === "assistant") {
        Object.assign(messages[i], update);
        break;
      }
    }
  }

  function getLastAssistant() {
    for (var i = messages.length - 1; i >= 0; i--) {
      if (messages[i].role === "assistant") return messages[i];
    }
    return null;
  }

  function rerenderLastAssistant() {
    var msg = getLastAssistant();
    if (!msg) return;
    var el = $messages.querySelector('[data-id="' + msg.id + '"]');
    if (el) {
      var fresh = renderMessage(msg);
      el.replaceWith(fresh);
    }
    scrollToBottom();
  }

  // --- SSE streaming ---

  async function sendMessage(content) {
    if (isStreaming || !content.trim()) return;
    isStreaming = true;
    $sendBtn.textContent = "Stop";
    $sendBtn.classList.add("stop-btn");
    $input.disabled = true;

    var userMsg = {
      id: uid(),
      role: "user",
      content: content,
      createdAt: new Date().toISOString(),
    };
    messages.push(userMsg);
    $messages.appendChild(renderMessage(userMsg));

    if ($messages.querySelector(".messages-empty")) {
      $messages.querySelector(".messages-empty").remove();
    }
    if ($messages.querySelector(".suggested-prompts")) {
      $messages.querySelector(".suggested-prompts").remove();
    }
    if ($messages.querySelector(".suggested-disclaimer")) {
      $messages.querySelector(".suggested-disclaimer").remove();
    }

    var assistantMsg = {
      id: uid(),
      role: "assistant",
      content: "",
      createdAt: new Date().toISOString(),
    };
    messages.push(assistantMsg);
    $messages.appendChild(renderMessage(assistantMsg));
    scrollToBottom();

    var body = { message: content };
    if (activeId) body.conversation_id = activeId;

    var controller = new AbortController();
    abortController = controller;

    try {
      var res = await fetch(API_URL + "/chat", {
        method: "POST",
        headers: headers(),
        signal: controller.signal,
        body: JSON.stringify(body),
      });

      if (res.status === 401) {
        messages.pop(); // remove placeholder assistant
        messages.pop(); // remove user message
        renderMessages();
        finishStreaming();
        await showLogin();
        sendMessage(content);
        return;
      }

      if (!res.ok || !res.body) {
        var errorMsg = "Unable to reach Skipper right now.";
        if (res.ok && !res.body) {
          errorMsg = "Streaming unavailable.";
        } else {
          try {
            var errBody = await res.json();
            if (res.status === 429) {
              var mins = Math.ceil((errBody.retry_after || 60) / 60);
              errorMsg =
                "Rate limit reached. Try again in " +
                mins +
                " minute" +
                (mins > 1 ? "s" : "") +
                ".";
            } else if (res.status === 403) {
              errorMsg = errBody.error || "Skipper requires a premium subscription.";
            } else if (errBody.error) {
              errorMsg = errBody.error;
            }
          } catch (_e) {}
        }
        updateLastAssistant({ content: errorMsg, confidence: "unknown" });
        rerenderLastAssistant();
        finishStreaming();
        return;
      }

      // Capture conversation ID from response header
      var newConvId = res.headers.get("X-Conversation-ID");
      if (newConvId && !activeId) {
        activeId = newConvId;
        loadConversations();
      }

      var reader = res.body.getReader();
      var decoder = new TextDecoder();
      var buffer = "";

      while (true) {
        var result = await reader.read();
        if (result.done) break;
        buffer += decoder.decode(result.value, { stream: true });
        var sepIdx = buffer.indexOf("\n\n");
        while (sepIdx !== -1) {
          var rawEvent = buffer.slice(0, sepIdx);
          buffer = buffer.slice(sepIdx + 2);
          var dataLines = rawEvent
            .split("\n")
            .filter(function (l) {
              return l.startsWith("data:");
            })
            .map(function (l) {
              return l.replace(/^data:\s?/, "");
            });
          var data = dataLines.join("\n").trim();

          if (data === "[DONE]") break;

          if (data) {
            var parsed = tryParseJSON(data);
            if (parsed && typeof parsed === "object") {
              if (parsed.type === "token" && typeof parsed.content === "string") {
                var cur = getLastAssistant();
                if (cur) cur.content += parsed.content;
                rerenderLastAssistant();
              } else if (parsed.type === "tool_start") {
                var ta = getLastAssistant();
                if (ta) {
                  if (!ta.tools) ta.tools = [];
                  ta.tools.push({ name: parsed.tool, status: "running" });
                  rerenderLastAssistant();
                }
              } else if (parsed.type === "tool_end") {
                var tb = getLastAssistant();
                if (tb && tb.tools) {
                  for (var ti = tb.tools.length - 1; ti >= 0; ti--) {
                    if (tb.tools[ti].name === parsed.tool && tb.tools[ti].status === "running") {
                      tb.tools[ti].status = parsed.error ? "errored" : "done";
                      break;
                    }
                  }
                  rerenderLastAssistant();
                }
              } else if (parsed.type === "meta") {
                updateLastAssistant({
                  confidence: parsed.confidence,
                  citations: parsed.citations,
                  externalLinks: parsed.externalLinks,
                  details: parsed.details,
                });
                rerenderLastAssistant();
              } else if (parsed.type === "done") {
                break;
              }
            }
          }
          sepIdx = buffer.indexOf("\n\n");
        }
      }
    } catch (err) {
      if (err instanceof DOMException && err.name === "AbortError") {
        var cur = getLastAssistant();
        if (cur && !cur.content.trim()) {
          updateLastAssistant({ content: "*Response stopped.*", confidence: "unknown" });
        }
        rerenderLastAssistant();
      } else {
        updateLastAssistant({
          content: "Connection error. Please try again.",
          confidence: "unknown",
        });
        rerenderLastAssistant();
      }
    }

    finishStreaming();
  }

  function stopStreaming() {
    if (abortController) {
      abortController.abort();
      abortController = null;
    }
  }

  function finishStreaming() {
    isStreaming = false;
    abortController = null;
    // Detect empty response from orchestrator crash
    var last = getLastAssistant();
    if (last && !last.content.trim()) {
      updateLastAssistant({
        content: "Something went wrong. Please try again.",
        confidence: "unknown",
      });
      rerenderLastAssistant();
    }
    $sendBtn.textContent = "Send";
    $sendBtn.classList.remove("stop-btn");
    $input.disabled = false;
    $input.focus();
  }

  function tryParseJSON(s) {
    try {
      return JSON.parse(s);
    } catch {
      return null;
    }
  }

  // --- Scroll management ---

  var userScrolledUp = false;

  function scrollToBottom() {
    requestAnimationFrame(function () {
      $messages.scrollTop = $messages.scrollHeight;
    });
  }

  function checkScroll() {
    var gap = $messages.scrollHeight - $messages.scrollTop - $messages.clientHeight;
    userScrolledUp = gap > 80;
    $scrollBtn.style.display = userScrolledUp ? "block" : "none";
  }

  // --- Dark/light toggle ---

  function getTheme() {
    var stored = localStorage.getItem("skipper-theme");
    if (stored) return stored;
    return window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
  }

  function setTheme(theme) {
    document.documentElement.setAttribute("data-theme", theme);
    localStorage.setItem("skipper-theme", theme);
    var btn = document.getElementById("theme-toggle");
    if (btn) btn.textContent = theme === "dark" ? "\u2600" : "\u263E";
  }

  function toggleTheme() {
    setTheme(getTheme() === "dark" ? "light" : "dark");
  }

  // --- Mobile sidebar ---

  function openSidebar() {
    $sidebar.classList.add("open");
    $overlay.classList.add("open");
  }

  function closeSidebar() {
    $sidebar.classList.remove("open");
    $overlay.classList.remove("open");
  }

  // --- Utilities ---

  function uid() {
    if (crypto && crypto.randomUUID) return crypto.randomUUID();
    return Date.now().toString(36) + Math.random().toString(36).slice(2);
  }

  function formatTime(dateStr) {
    if (!dateStr) return "";
    try {
      var d = new Date(dateStr);
      if (isNaN(d.getTime())) return "";
      var h = d.getHours();
      var m = d.getMinutes();
      var ampm = h >= 12 ? "PM" : "AM";
      h = h % 12 || 12;
      return h + ":" + (m < 10 ? "0" : "") + m + " " + ampm;
    } catch {
      return "";
    }
  }

  function relativeTime(dateStr) {
    if (!dateStr) return "";
    try {
      var d = new Date(dateStr);
      if (isNaN(d.getTime())) return "";
      var now = Date.now();
      var diff = Math.floor((now - d.getTime()) / 1000);
      if (diff < 60) return "just now";
      if (diff < 3600) return Math.floor(diff / 60) + "m ago";
      if (diff < 86400) return Math.floor(diff / 3600) + "h ago";
      if (diff < 604800) return Math.floor(diff / 86400) + "d ago";
      return d.toLocaleDateString();
    } catch {
      return "";
    }
  }

  // --- Init ---

  function init() {
    $sidebar = document.getElementById("sidebar");
    $list = document.getElementById("conv-list");
    $messages = document.getElementById("messages");
    $input = document.getElementById("chat-input");
    $sendBtn = document.getElementById("send-btn");
    $scrollBtn = document.getElementById("scroll-btn");
    $overlay = document.getElementById("sidebar-overlay");
    $convTitle = document.getElementById("conv-title");

    setTheme(getTheme());

    document.getElementById("new-chat-btn").onclick = newChat;
    document.getElementById("theme-toggle").onclick = toggleTheme;
    document.getElementById("mobile-menu").onclick = openSidebar;
    $overlay.onclick = closeSidebar;
    $scrollBtn.onclick = scrollToBottom;

    $messages.addEventListener("scroll", checkScroll);

    $messages.addEventListener("click", function (e) {
      var btn = e.target.closest("[data-copy-target]");
      if (!btn) return;
      var pre = document.getElementById(btn.dataset.copyTarget);
      if (!pre) return;
      navigator.clipboard.writeText(pre.textContent || "");
      btn.textContent = "Copied!";
      setTimeout(function () {
        btn.textContent = "Copy";
      }, 1500);
    });

    $sendBtn.onclick = function () {
      if (isStreaming) {
        stopStreaming();
        return;
      }
      var val = $input.value.trim();
      if (val) {
        $input.value = "";
        $input.style.height = "auto";
        sendMessage(val);
      }
    };

    $input.addEventListener("keydown", function (e) {
      if (e.key === "Enter" && !e.shiftKey) {
        e.preventDefault();
        $sendBtn.click();
      }
    });

    $input.addEventListener("input", function () {
      this.style.height = "auto";
      this.style.height = Math.min(this.scrollHeight, 160) + "px";
    });

    loadConversations();
    renderMessages();
    $input.focus();
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", init);
  } else {
    init();
  }
})();
