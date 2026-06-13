(function() {
    var chatRenderer = null;

    function byId(id) {
        return document.getElementById(id);
    }

    function nowHHMM() {
        var d = new Date();
        var hh = String(d.getHours()).padStart(2, '0');
        var mm = String(d.getMinutes()).padStart(2, '0');
        return hh + ':' + mm;
    }

    function scrollMessages() {
        var messages = byId('chat2-messages');
        if (!messages) return;
        messages.scrollTo({ top: messages.scrollHeight, behavior: 'smooth' });
    }

    function setConnState(state) {
        var badge = byId('chat2-conn-badge');
        if (!badge) return;
        badge.textContent = state;
    }

    function escapeHTML(str) {
        var div = document.createElement('div');
        div.appendChild(document.createTextNode(str || ''));
        return div.innerHTML;
    }

    function setupMarkdownRenderer() {
        if (!window.marked) return;
        chatRenderer = new marked.Renderer();
        chatRenderer.code = function(obj) {
            var code = (typeof obj === 'object') ? obj.text : obj;
            var lang = (typeof obj === 'object') ? (obj.lang || '') : (arguments[1] || '');
            lang = lang.split(/\s/)[0] || 'text';
            var highlighted = '';
            if (window.hljs) {
                if (hljs.getLanguage(lang)) {
                    highlighted = hljs.highlight(code, { language: lang }).value;
                } else {
                    highlighted = hljs.highlightAuto(code).value;
                }
            } else {
                highlighted = escapeHTML(code);
            }
            return '<div class="chat-code-block">' +
                '<div class="chat-code-header"><span>' + escapeHTML(lang) + '</span>' +
                '<button class="chat-code-copy" onclick="copyCode(this)">Copy</button></div>' +
                '<pre><code>' + highlighted + '</code></pre></div>';
        };
        marked.setOptions({ renderer: chatRenderer, breaks: true, gfm: true });
    }

    function renderMarkdown(text) {
        if (!text) return '';
        if (!window.marked) return escapeHTML(text);
        var html = marked.parse(text);
        if (window.DOMPurify) {
            html = DOMPurify.sanitize(html, { ADD_ATTR: ['target', 'rel'] });
        }
        html = html.replace(/<a /g, '<a target="_blank" rel="noopener" ');
        return html;
    }

    function renderExistingMarkdown() {
        var els = document.querySelectorAll('#chat2-messages .chat-md-content:not([data-rendered])');
        for (var i = 0; i < els.length; i++) {
            var el = els[i];
            var raw = el.textContent;
            el.innerHTML = renderMarkdown(raw);
            el.setAttribute('data-rendered', '1');
        }
    }

    function showTypingIndicator() {
        removeTypingIndicator();
        var messages = byId('chat2-messages');
        if (!messages) return;
        var div = document.createElement('div');
        div.id = 'chat2-typing';
        div.className = 'chat-typing';
        div.innerHTML = '<div class="chat-typing-dot"></div>' +
                        '<div class="chat-typing-dot"></div>' +
                        '<div class="chat-typing-dot"></div>';
        messages.appendChild(div);
        scrollMessages();
    }

    function removeTypingIndicator() {
        var el = byId('chat2-typing');
        if (el) el.remove();
    }

    function appendUserMessage(content, timestamp) {
        var messages = byId('chat2-messages');
        if (!messages) return;

        var empty = messages.querySelector('.chat-empty-state');
        if (empty) empty.remove();

        var msg = document.createElement('div');
        msg.className = 'chat-msg chat-msg-user';

        var body = document.createElement('div');
        body.className = 'chat-msg-content';
        body.textContent = content;
        msg.appendChild(body);

        var ts = document.createElement('div');
        ts.className = 'chat-msg-time';
        ts.textContent = timestamp || nowHHMM();
        msg.appendChild(ts);

        messages.appendChild(msg);
        scrollMessages();
    }

    function createAssistantBubble(agentName) {
        var messages = byId('chat2-messages');
        if (!messages) return null;

        var row = document.createElement('div');
        row.className = 'chat-msg-agent-row';

        var avatar = document.createElement('div');
        avatar.className = 'chat-msg-avatar';
        avatar.textContent = agentName ? agentName.charAt(0).toUpperCase() : 'A';
        row.appendChild(avatar);

        var bubble = document.createElement('div');
        bubble.className = 'chat-msg chat-msg-agent';

        var body = document.createElement('div');
        body.className = 'chat-msg-content chat-md-content';
        bubble.appendChild(body);

        row.appendChild(bubble);
        messages.appendChild(row);
        scrollMessages();
        return { row: row, bubble: bubble, body: body };
    }

    function appendAssistantMessage(agentName, content, timestamp, asPlain) {
        var messages = byId('chat2-messages');
        if (!messages) return;
        var empty = messages.querySelector('.chat-empty-state');
        if (empty) empty.remove();

        var els = createAssistantBubble(agentName);
        if (!els) return;
        if (asPlain) {
            els.body.textContent = content || '';
        } else {
            els.body.innerHTML = renderMarkdown(content || '');
            els.body.setAttribute('data-rendered', '1');
        }

        var ts = document.createElement('div');
        ts.className = 'chat-msg-time';
        ts.textContent = timestamp || nowHHMM();
        els.bubble.appendChild(ts);
        scrollMessages();
    }

    function appendSystemError(text) {
        appendAssistantMessage('!', 'Error: ' + text, nowHHMM(), false);
    }

    window.copyCode = function(btn) {
        var codeBlock = btn.closest('.chat-code-block');
        if (!codeBlock) return;
        var code = codeBlock.querySelector('code');
        if (!code || !navigator.clipboard) return;
        navigator.clipboard.writeText(code.textContent).then(function() {
            var orig = btn.textContent;
            btn.textContent = 'Copied!';
            setTimeout(function() { btn.textContent = orig; }, 1500);
        });
    };

    function wsURL(agentID) {
        var proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
        return proto + '//' + window.location.host + '/agents/' + encodeURIComponent(agentID) + '/chat2/ws';
    }

    function makeID() {
        return String(Date.now()) + '-' + Math.random().toString(16).slice(2, 10);
    }

    window.initKyvikChatV2 = function(cfg) {
        var form = byId('chat2-form');
        var input = byId('chat2-input');
        var convInput = byId('chat2-conv-id');
        if (!form || !input || !convInput) return;

        var ws = null;
        var reconnectTimer = null;
        var reconnectAttempt = 0;
        var manualClose = false;
        var appPingTimer = null;

        var currentConvID = cfg.conversationID || '';
        var streaming = null;
        var streamingRaw = '';
        var streamingReqID = '';
        var lastDoneReqID = '';
        var viewMode = localStorage.getItem('kyvik-chat2-view-mode') || 'user';
        var pendingHistorySync = null;
        var historySyncQueued = false;

        function setSidebarActive(convID) {
            var nodes = document.querySelectorAll('#chat2-sidebar-list .chat-sidebar-entry');
            for (var i = 0; i < nodes.length; i++) {
                nodes[i].classList.remove('active');
            }
            if (!convID) return;
            var active = document.querySelector('#chat2-sidebar-list .chat-sidebar-entry[data-conv-id="' + convID + '"]');
            if (active) active.classList.add('active');
        }

        function refreshModeButtons() {
            var userBtn = byId('chat2-mode-user');
            var debugBtn = byId('chat2-mode-debug');
            if (userBtn) userBtn.classList.toggle('btn-primary', viewMode === 'user');
            if (userBtn) userBtn.classList.toggle('btn-secondary', viewMode !== 'user');
            if (debugBtn) debugBtn.classList.toggle('btn-primary', viewMode === 'debug');
            if (debugBtn) debugBtn.classList.toggle('btn-secondary', viewMode !== 'debug');
        }

        function getSidebarTitle(convID) {
            if (!convID) return '';
            var node = document.querySelector('#chat2-sidebar-list .chat-sidebar-entry[data-conv-id="' + convID + '"]');
            if (!node) return '';
            return node.getAttribute('data-conv-title') || '';
        }

        function setHeaderTitle(titleText) {
            var title = byId('chat2-header-title');
            if (!title) return;
            title.textContent = titleText || '';
        }

        function setViewMode(mode) {
            viewMode = (mode === 'debug') ? 'debug' : 'user';
            localStorage.setItem('kyvik-chat2-view-mode', viewMode);
            refreshModeButtons();
            if (pendingHistorySync) {
                applyHistorySync(pendingHistorySync);
            } else {
                requestHistorySync();
            }
        }

        function addSidebarConversation(convID, titleText) {
            var list = byId('chat2-sidebar-list');
            if (!list || !convID) return;
            if (document.querySelector('#chat2-sidebar-list .chat-sidebar-entry[data-conv-id="' + convID + '"]')) {
                return;
            }
            var empty = list.querySelector('.chat-sidebar-empty');
            if (empty) empty.remove();

            var group = list.querySelector('.chat-sidebar-group');
            if (!group) {
                group = document.createElement('div');
                group.className = 'chat-sidebar-group';

                var title = document.createElement('div');
                title.className = 'chat-sidebar-group-title';
                title.textContent = 'Today';
                group.appendChild(title);
                list.prepend(group);
            }

            var entry = document.createElement('div');
            entry.className = 'chat-sidebar-entry';
            entry.setAttribute('data-conv-id', convID);
            entry.setAttribute('data-conv-title', titleText || 'New conversation');
            entry.onclick = function() { window.kyvikChat2SwitchConversation(convID); };

            var inner = document.createElement('div');
            inner.className = 'chat-sidebar-entry-title';
            inner.textContent = titleText || 'New conversation';
            entry.appendChild(inner);

            if (group.children.length > 1) {
                group.insertBefore(entry, group.children[1]);
            } else {
                group.appendChild(entry);
            }
        }

        function scheduleReconnect() {
            if (manualClose || reconnectTimer) return;
            reconnectAttempt++;
            var delay = Math.min(1000 * reconnectAttempt, 5000);
            setConnState('reconnecting');
            reconnectTimer = setTimeout(function() {
                reconnectTimer = null;
                connect();
            }, delay);
        }

        function connect() {
            if (!cfg.running) {
                setConnState('agent-stopped');
                return;
            }
            ws = new WebSocket(wsURL(cfg.agentID));

            ws.addEventListener('open', function() {
                reconnectAttempt = 0;
                setConnState('connected');
                if (appPingTimer) clearInterval(appPingTimer);
                appPingTimer = setInterval(function() {
                    if (ws && ws.readyState === WebSocket.OPEN) {
                        ws.send(JSON.stringify({ type: 'ping' }));
                    }
                }, 30000);
                requestHistorySync();
            });

            ws.addEventListener('close', function() {
                if (appPingTimer) {
                    clearInterval(appPingTimer);
                    appPingTimer = null;
                }
                if (manualClose) return;
                scheduleReconnect();
            });

            ws.addEventListener('error', function() {
                // Allow close + reconnect path to handle transient issues.
            });

            ws.addEventListener('message', function(msg) {
                var ev;
                try {
                    ev = JSON.parse(msg.data);
                } catch (e) {
                    appendSystemError('invalid server event');
                    return;
                }
                handleEvent(ev);
            });
        }

        function sameConv(convID) {
            return !convID || !currentConvID || convID === currentConvID;
        }

        function finalizeStreaming(timestamp) {
            if (streaming) {
                if (viewMode === 'user' && !streamingRaw.trim()) {
                    streaming.row.remove();
                } else {
                    var ts = document.createElement('div');
                    ts.className = 'chat-msg-time';
                    ts.textContent = timestamp || nowHHMM();
                    streaming.bubble.appendChild(ts);
                }
            }
            streaming = null;
            streamingRaw = '';
            streamingReqID = '';
            removeTypingIndicator();
            scrollMessages();
        }

        function requestHistorySync() {
            if (!ws || ws.readyState !== WebSocket.OPEN) return;
            ws.send(JSON.stringify({
                type: 'history_sync',
                conversation_id: currentConvID,
                since_id: 0
            }));
        }

        function selectHistoryMessages(ev) {
            if (viewMode === 'debug' && Array.isArray(ev.messages_debug)) {
                return ev.messages_debug;
            }
            if (Array.isArray(ev.messages_user_visible)) {
                return ev.messages_user_visible;
            }
            return ev.messages || [];
        }

        function renderHistory(messages) {
            var container = byId('chat2-messages');
            if (!container) return;
            container.innerHTML = '';

            if (!messages || messages.length === 0) {
                var empty = document.createElement('div');
                empty.className = 'chat-empty-state';
                empty.innerHTML = '<p>No messages yet in this conversation.</p>';
                container.appendChild(empty);
                removeTypingIndicator();
                return;
            }

            for (var i = 0; i < messages.length; i++) {
                var m = messages[i];
                var content = m.content || '';
                if (m.role === 'user') {
                    appendUserMessage(content, m.timestamp || nowHHMM());
                    continue;
                }
                if (viewMode === 'debug' && m.role !== 'assistant') {
                    var prefix = '[' + (m.role || 'internal') + '] ';
                    appendAssistantMessage(cfg.agentName, prefix + (content || '[empty]'), m.timestamp || nowHHMM(), true);
                    continue;
                }
                appendAssistantMessage(cfg.agentName, content, m.timestamp || nowHHMM(), false);
            }

            renderExistingMarkdown();
            removeTypingIndicator();
            scrollMessages();
        }

        function applyHistorySync(ev) {
            pendingHistorySync = ev;
            if (streaming) {
                historySyncQueued = true;
                return;
            }
            historySyncQueued = false;
            renderHistory(selectHistoryMessages(ev));
        }

        function handleEvent(ev) {
            switch (ev.type) {
            case 'connected':
            case 'pong':
                break;
            case 'ack':
                if (!currentConvID && ev.conversation_id) {
                    currentConvID = ev.conversation_id;
                    convInput.value = currentConvID;
                }
                break;
            case 'conversation_created':
                if (ev.conversation_id) {
                    currentConvID = ev.conversation_id;
                    convInput.value = currentConvID;
                    var u = '/agents/' + encodeURIComponent(cfg.agentID) + '/chat2?c=' + encodeURIComponent(currentConvID);
                    window.history.replaceState(null, '', u);
                    addSidebarConversation(currentConvID, 'New conversation');
                    setSidebarActive(currentConvID);
                    setHeaderTitle(getSidebarTitle(currentConvID));
                    var sidebar = byId('chat2-sidebar');
                    if (sidebar) sidebar.classList.remove('open');
                    requestHistorySync();
                }
                break;
            case 'history_sync':
                if (!sameConv(ev.conversation_id)) break;
                applyHistorySync(ev);
                break;
            case 'history_sync_error':
                appendSystemError(ev.error || 'failed to sync history');
                break;
            case 'assistant_chunk':
                if (!sameConv(ev.conversation_id)) break;
                var chunk = ev.content || '';
                if (viewMode === 'user' && !chunk.trim()) break;
                removeTypingIndicator();
                if (!streaming) {
                    streaming = createAssistantBubble(cfg.agentName);
                    streamingRaw = '';
                }
                if (ev.request_id) {
                    streamingReqID = ev.request_id;
                }
                streamingRaw += chunk;
                if (streaming && streaming.body) {
                    if (viewMode === 'debug') {
                        streaming.body.textContent = streamingRaw;
                    } else {
                        streaming.body.innerHTML = renderMarkdown(streamingRaw);
                        streaming.body.setAttribute('data-rendered', '1');
                    }
                }
                scrollMessages();
                break;
            case 'assistant_done':
                if (!sameConv(ev.conversation_id)) break;
                if (ev.request_id) {
                    lastDoneReqID = ev.request_id;
                }
                finalizeStreaming(ev.timestamp);
                if (historySyncQueued && pendingHistorySync) {
                    applyHistorySync(pendingHistorySync);
                }
                break;
            case 'assistant_message':
                if (!sameConv(ev.conversation_id)) break;
                if (ev.request_id && ev.request_id === lastDoneReqID) break;
                if (ev.request_id && streamingReqID && ev.request_id === streamingReqID) break;
                if (viewMode === 'user' && !(ev.content || '').trim()) break;
                removeTypingIndicator();
                appendAssistantMessage(cfg.agentName, ev.content || '', ev.timestamp, viewMode === 'debug');
                break;
            case 'assistant_error':
            case 'event_error':
                finalizeStreaming(ev.timestamp);
                appendSystemError(ev.error || 'unknown error');
                if (historySyncQueued && pendingHistorySync) {
                    applyHistorySync(pendingHistorySync);
                }
                break;
            default:
                appendSystemError('unsupported event type: ' + ev.type);
            }
        }

        form.addEventListener('submit', function(e) {
            e.preventDefault();
            if (!ws || ws.readyState !== WebSocket.OPEN) {
                appendSystemError('connection unavailable, retrying');
                scheduleReconnect();
                return;
            }

            var content = (input.value || '').trim();
            if (!content) return;

            appendUserMessage(content, nowHHMM());
            showTypingIndicator();
            var reqID = makeID();
            ws.send(JSON.stringify({
                type: 'user_message',
                request_id: reqID,
                content: content,
                conversation_id: currentConvID
            }));
            input.value = '';
            input.style.height = 'auto';
            input.focus();
        });

        input.addEventListener('keydown', function(e) {
            if (e.key === 'Enter' && !e.shiftKey) {
                e.preventDefault();
                form.requestSubmit();
            }
        });

        input.addEventListener('input', function() {
            input.style.height = 'auto';
            input.style.height = Math.min(input.scrollHeight, 160) + 'px';
        });

        window.addEventListener('beforeunload', function() {
            manualClose = true;
            if (reconnectTimer) clearTimeout(reconnectTimer);
            if (appPingTimer) clearInterval(appPingTimer);
            if (ws) ws.close();
        });

        window.kyvikChat2SwitchConversation = function(convID) {
            finalizeStreaming();
            currentConvID = convID || '';
            convInput.value = currentConvID;
            setSidebarActive(currentConvID);
            var u = '/agents/' + encodeURIComponent(cfg.agentID) + '/chat2';
            if (currentConvID) {
                u += '?c=' + encodeURIComponent(currentConvID);
            }
            window.history.pushState(null, '', u);
            setHeaderTitle(getSidebarTitle(currentConvID));
            var sidebar = byId('chat2-sidebar');
            if (sidebar) sidebar.classList.remove('open');
            requestHistorySync();
        };

        window.kyvikChat2StartNew = function() {
            finalizeStreaming();
            currentConvID = '';
            convInput.value = '';
            setSidebarActive('');
            setHeaderTitle('');
            var container = byId('chat2-messages');
            if (container) {
                container.innerHTML = '<div class="chat-empty-state"><p>Start a conversation by typing a message below.</p></div>';
            }
            window.history.pushState(null, '', '/agents/' + encodeURIComponent(cfg.agentID) + '/chat2');
            var sidebar = byId('chat2-sidebar');
            if (sidebar) sidebar.classList.remove('open');
        };

        setupMarkdownRenderer();
        renderExistingMarkdown();
        refreshModeButtons();
        var modeUserBtn = byId('chat2-mode-user');
        var modeDebugBtn = byId('chat2-mode-debug');
        if (modeUserBtn) modeUserBtn.addEventListener('click', function() { setViewMode('user'); });
        if (modeDebugBtn) modeDebugBtn.addEventListener('click', function() { setViewMode('debug'); });
        convInput.value = currentConvID;
        setSidebarActive(currentConvID);
        setHeaderTitle(getSidebarTitle(currentConvID));
        connect();
    };
})();
