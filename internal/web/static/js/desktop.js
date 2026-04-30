import RFB from '/static/vendor/noVNC/core/rfb.js';

var targetName = window.HTTPMUX_TARGET;
var statusEl = document.getElementById('conn-status');
var containerEl = document.getElementById('vnc-container');

var proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
var url = proto + '//' + location.host + '/ws/desktop/' + targetName;

var rfb = new RFB(containerEl, url, { wsProtocols: ['binary'] });
rfb.viewOnly = false;
rfb.scaleViewport = true;
rfb.resizeSession = true;

rfb.addEventListener('connect', function() {
    statusEl.className = 'conn-status connected';
    statusEl.textContent = 'connected';
    setTimeout(function() { statusEl.style.opacity = '0'; }, 2000);
});

rfb.addEventListener('disconnect', function(e) {
    statusEl.style.opacity = '1';
    statusEl.className = 'conn-status disconnected';
    if (e.detail.clean) {
        statusEl.textContent = 'disconnected';
    } else {
        statusEl.textContent = 'disconnected — reconnecting...';
        setTimeout(function() { location.reload(); }, 3000);
    }
});

rfb.addEventListener('credentialsrequired', function() {
    var password = prompt('VNC password:');
    if (password) {
        rfb.sendCredentials({ password: password });
    }
});
