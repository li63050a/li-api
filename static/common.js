// common.js — 扩展功能页共享助手（由 /index.html 的扩展面板通过 iframe 加载）
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
        document.body.innerHTML = '<p style="padding:24px">未登录，请先 <a href="/index.html">返回管理后台登录</a>。</p>';
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

function $(id) { return document.getElementById(id); }
function v(id) { return document.getElementById(id).value; }
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
