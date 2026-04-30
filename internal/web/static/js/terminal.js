(function() {
    'use strict';

    var DATA = 0x00;
    var RESIZE = 0x01;
    var targetName = window.HTTPMUX_TARGET;
    var statusEl = document.getElementById('conn-status');
    var containerEl = document.getElementById('terminal-container');

    var term = new Terminal({
        cursorBlink: true,
        fontSize: 14,
        fontFamily: '"SF Mono", "Fira Code", "Cascadia Code", Menlo, monospace',
        theme: { background: '#1a1b26', foreground: '#c0caf5' }
    });

    var fitAddon = new FitAddon.FitAddon();
    term.loadAddon(fitAddon);
    term.loadAddon(new WebLinksAddon.WebLinksAddon());
    term.open(containerEl);
    fitAddon.fit();

    var proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    var ws = new WebSocket(proto + '//' + location.host + '/ws/terminal/' + targetName);
    ws.binaryType = 'arraybuffer';

    function sendData(data) {
        if (ws.readyState !== WebSocket.OPEN) return;
        var encoder = new TextEncoder();
        var payload = encoder.encode(data);
        var msg = new Uint8Array(1 + payload.length);
        msg[0] = DATA;
        msg.set(payload, 1);
        ws.send(msg.buffer);
    }

    function sendResize() {
        fitAddon.fit();
        if (ws.readyState !== WebSocket.OPEN) return;
        var dims = JSON.stringify({ cols: term.cols, rows: term.rows });
        var encoder = new TextEncoder();
        var payload = encoder.encode(dims);
        var msg = new Uint8Array(1 + payload.length);
        msg[0] = RESIZE;
        msg.set(payload, 1);
        ws.send(msg.buffer);
    }

    ws.onopen = function() {
        statusEl.className = 'conn-status connected';
        statusEl.textContent = 'connected';
        sendResize();
        setTimeout(function() { statusEl.style.opacity = '0'; }, 2000);
    };

    ws.onmessage = function(event) {
        term.write(new Uint8Array(event.data));
    };

    ws.onclose = function() {
        statusEl.style.opacity = '1';
        statusEl.className = 'conn-status disconnected';
        statusEl.textContent = 'disconnected — reconnecting...';
        term.write('\r\n\x1b[33m[Connection lost. Reconnecting...]\x1b[0m\r\n');
        setTimeout(function() { location.reload(); }, 3000);
    };

    ws.onerror = function() {
        statusEl.className = 'conn-status disconnected';
        statusEl.textContent = 'connection error';
    };

    term.onData(sendData);

    var resizeTimer;
    window.addEventListener('resize', function() {
        clearTimeout(resizeTimer);
        resizeTimer = setTimeout(sendResize, 100);
    });

    term.focus();
})();
