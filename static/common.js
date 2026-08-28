// common.js — 扩展功能页共享助手（由管理台 index.html 通过 iframe 加载）
const GW_TOKEN = 'gw_token';
function getToken() { return localStorage.getItem(GW_TOKEN) || ''; }
function setToken(t) { localStorage.setItem(GW_TOKEN, t); }
function clearToken() { localStorage.removeItem(GW_TOKEN); }

// 统一带鉴权的请求
async function api(path, opts = {}) {
    opts.headers = opts.headers || {};
    const t = getToken();
    if (t) opts.headers['Authorization'] = 'Bearer ' + t;
    return fetch(path, opts);
}

// 鉴权检查：未登录则提示并返回登录页
async function ensureAuth() {
    const r = await api('/api/user/self');
    if (!r.ok) {
        document.body.innerHTML = '<div class="card" style="max-width:420px;margin:60px auto"><p>未登录，请先 <a href="/index.html">返回管理后台登录</a>。</p></div>';
        throw new Error('no auth');
    }
    return r.json();
}

// 主题同步：与 index.html 的暗色切换共用 localStorage 键 gw_theme
(function () {
    if (localStorage.getItem('gw_theme') === 'dark') {
        document.documentElement.classList.add('dark');
    }
})();

// 通知父窗口调整 iframe 高度（同源，直接测量即可）
function notifyHeight() {
    const h = Math.max(
        document.body ? document.body.scrollHeight : 0,
        document.documentElement ? document.documentElement.scrollHeight : 0
    );
    try {
        if (window.parent && window.parent !== window && window.parent.postMessage) {
            window.parent.postMessage({ type: 'gw_frame_height', height: h }, '*');
        }
    } catch (e) { /* ignore */ }
}

function $(id) { return document.getElementById(id); }
function v(id) { return document.getElementById(id).value; }

// 空态：统一展示「暂无数据」。对 tbody 自动生成全宽空行，其余容器渲染 .empty
function emptyState(el, text) {
    if (!el) return;
    const t = escapeHtml(text == null ? '暂无数据' : String(text));
    if (el.tagName === 'TBODY') {
        let cols = 1;
        const tbl = el.closest('table');
        if (tbl) cols = tbl.querySelectorAll('thead th').length;
        el.innerHTML = '<tr><td colspan="' + Math.max(1, cols) + '" class="muted">' + t + '</td></tr>';
        return;
    }
    el.innerHTML = '<div class="empty"><p class="muted">' + t + '</p></div>';
}

// 加载态：spinner + 文案，用于「spinner -> 结果」模式
function loadState(el, text) {
    if (!el) return;
    const t = escapeHtml(text == null ? '加载中…' : String(text));
    if (el.tagName === 'TBODY') {
        let cols = 1;
        const tbl = el.closest('table');
        if (tbl) cols = tbl.querySelectorAll('thead th').length;
        el.innerHTML = '<tr><td colspan="' + Math.max(1, cols) + '" class="muted"><span class="spin"></span>' + t + '</td></tr>';
        return;
    }
    el.innerHTML = '<div class="empty"><p class="muted"><span class="spin"></span>' + t + '</p></div>';
}

// 带超时守卫的请求：超时自动中断，避免「加载中…」卡死
async function apiWithTimeout(path, opts, ms) {
    const ctrl = new AbortController();
    const timer = setTimeout(() => ctrl.abort(), ms || 12000);
    try {
        return await api(path, Object.assign({}, opts, { signal: ctrl.signal }));
    } finally {
        clearTimeout(timer);
    }
}
function fmtTime(s) {
    if (!s) return '';
    const d = new Date(s);
    if (isNaN(d.getTime())) return s;
    const p = n => String(n).padStart(2, '0');
    return d.getFullYear() + '-' + p(d.getMonth() + 1) + '-' + p(d.getDate()) + ' ' + p(d.getHours()) + ':' + p(d.getMinutes()) + ':' + p(d.getSeconds());
}
function maskTok(s) { if (!s) return ''; return s.length > 8 ? s.slice(0, 4) + '…' + s.slice(-4) : s; }
async function logout() { clearToken(); location.href = '/index.html'; }
function escapeHtml(s) {
    return String(s == null ? '' : s).replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}

// 页面加载后上报高度；DOM 变化时重新上报
(function () {
    if (window.self === window.top) return;
    function report() { notifyHeight(); }
    window.addEventListener('load', report);
    if (typeof ResizeObserver !== 'undefined') {
        try {
            const ro = new ResizeObserver(report);
            ro.observe(document.body);
        } catch (e) { /* ignore */ }
    }
    setInterval(report, 1500);
})();