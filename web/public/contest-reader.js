// This function runs as a bookmark on the competition site, in the user's browser.
function prtseekContestReader(appOrigin) {
  if (!/^https?:$/.test(location.protocol) || location.origin === appOrigin) {
    alert("请先打开已登录的赛事题目页，再点击取题书签。");
    return;
  }
  const selection = window.getSelection()?.toString().trim().slice(0, 32000);
  const portal = window.open(appOrigin + "/contest-import/#" + encodeURIComponent(location.origin), "_blank");
  if (!portal) {
    alert("请允许本页弹出 prtseek 取题窗口，然后重试。");
    return;
  }
  const base = new URL(location.href);
  base.search = base.hash = "";
  const match = base.pathname.match(/^(.*\/)challenges\/?$/);
  const apiRoot = new URL((match ? match[1] : "/") + "api/v1/challenges", base);
  const fileLimit = 64 * 1024 * 1024;
  const listed = new Set();

  function request(url, limit, binary, name = "") {
    return new Promise((resolve, reject) => {
      const xhr = new XMLHttpRequest();
      xhr.open("GET", url);
      xhr.responseType = binary ? "blob" : "text";
      xhr.timeout = 90000;
      xhr.onprogress = event => {
        if (event.loaded > limit) xhr.abort();
      };
      xhr.onerror = () => reject(Error("下载被浏览器或附件站拒绝，请核对登录状态和下载地址。"));
      xhr.ontimeout = () => reject(Error("请求超时，可重新拉取。"));
      xhr.onabort = () => reject(Error("内容超出导入大小限制。"));
      xhr.onload = () => {
        if (xhr.status === 401 || xhr.status === 403) return reject(Error("赛事登录已过期或账号无权查看，请回到官网登录。"));
        if (xhr.status < 200 || xhr.status >= 300) return reject(Error("赛事请求失败（HTTP " + xhr.status + "）。"));
        if (/\btext\/html\b/i.test(xhr.getResponseHeader("Content-Type") || "") && (!binary || !/\.html?$/i.test(name) || /\/(login|signin)(\/|$)/i.test(new URL(xhr.responseURL).pathname))) {
          return reject(Error("赛事返回了网页，请检查登录状态或改用「当前题面」。"));
        }
        if ((binary ? xhr.response.size : xhr.response.length) > limit) return reject(Error("内容超出导入大小限制。"));
        resolve(xhr.response);
      };
      xhr.send();
    });
  }
  async function api(url) {
    const body = await request(url.href, 4 * 1024 * 1024, false);
    let result;
    try { result = JSON.parse(body); } catch { throw Error("未收到 CTFd 接口响应，请改用「当前题面」。"); }
    if (result.success !== true) throw Error("赛事未返回题目。请核对登录状态与比赛开放时间。");
    return result.data;
  }
  function plain(html) {
    if (!/<(?:p|br|div|li|pre|span|a|img|code)\b/i.test(String(html || ""))) return String(html || "").trim().slice(0, 32000);
    const template = document.createElement("template");
    template.innerHTML = String(html || "");
    template.content.querySelectorAll("script,style,form").forEach(el => el.remove());
    template.content.querySelectorAll("p,div,br,li,pre").forEach(el => el.append("\n"));
    return (template.content.textContent || "").trim().slice(0, 32000);
  }
  function attachment(href, name) {
    const url = new URL(href, location.href);
    if (!/^https?:$/.test(url.protocol) || url.username || url.password) throw Error("附件地址无效。");
    let fallback = url.pathname.split("/").pop() || "attachment";
    try { fallback = decodeURIComponent(fallback); } catch {}
    const filename = (name || fallback).replace(/[<>:"/\\|?*\u0000-\u001f]/g, "_").slice(0, 180).trim();
    return { url, name: filename || "attachment" };
  }
  function currentPage() {
    const visible = el => el.getClientRects().length > 0;
    const scope = Array.from(document.querySelectorAll('[role="dialog"],dialog[open],.modal.show')).find(visible)
      || document.querySelector("article,main") || document.body;
    const title = (scope.querySelector("h1,.challenge-name,h2,h3")?.textContent || document.title).trim().slice(0, 240);
    const desc = scope.querySelector(".challenge-description,.challenge-desc,.problem-description,[data-challenge-description],article");
    const description = selection || desc?.innerText?.trim().slice(0, 32000) ||
      (scope.matches("article") ? scope.innerText.trim().slice(0, 32000) : "");
    const files = new Map();
    for (const a of scope.querySelectorAll("a[href]")) {
      if (!visible(a) || !/^https?:$/.test(a.protocol)) continue;
      if (!(a.hasAttribute("download") || /\.(zip|7z|rar|gz|tar|xz|pdf|txt|pcapng?|bin|dat|py|png|jpg)$/i.test(a.pathname) ||
        /附件|下载附件|download attachment/i.test(a.innerText))) continue;
      files.set(a.href, attachment(a.href, a.download));
    }
    if (!description && !files.size) throw Error("未识别到题面。请在赛事页面选中题目正文，再点击取题书签。");
    return { id: "page", title, category: "", description, files: [...files.values()] };
  }
  let page;
  async function respond(action, id) {
    if (action === "list-page") {
      page = currentPage();
      return [{ id: "page", title: page.title, category: "当前题面" }];
    }
    if (action === "list-ctfd") {
      const rows = await api(apiRoot);
      if (!Array.isArray(rows) || rows.length > 2000) throw Error("CTFd 题单格式或数量异常。");
      listed.clear();
      return rows.map(row => {
        if (!Number.isSafeInteger(row.id) || row.id <= 0 || typeof row.name !== "string") throw Error("题单数据无效。");
        const id = String(row.id);
        listed.add(id);
        return { id, title: row.name.slice(0, 240), category: String(row.category || "").slice(0, 120) };
      });
    }
    if (action !== "read" || !(id === "page" ? page : listed.has(id))) throw Error("请先读取题单并选择题目。");
    let item = page;
    if (id !== "page") {
      const data = await api(new URL(apiRoot.href + "/" + id));
      if (String(data.id) !== id || !Array.isArray(data.files || [])) throw Error("题目详情无效。");
      item = { id, title: String(data.name).slice(0, 240), category: String(data.category || "").slice(0, 120),
        description: plain(data.description), files: (data.files || []).map(href => attachment(href)) };
    }
    if (item.files.length > 32) throw Error("单题附件超过 32 个。");
    const files = [];
    let remaining = fileLimit;
    for (const file of item.files) {
      const blob = await request(file.url.href, remaining, true, file.name);
      remaining -= blob.size;
      files.push(new File([blob], file.name, { type: "application/octet-stream" }));
    }
    const scope = id === "page" ? [base.pathname, item.title] : [apiRoot.pathname, id];
    return { key: JSON.stringify([location.origin, ...scope]), title: item.title, category: item.category,
      description: item.description, source: location.origin, files };
  }
  function connect(event) {
    if (event.source !== portal || event.origin !== appOrigin || event.data !== "prtseek-contest-ready-v1" || !event.ports[0]) return;
    window.removeEventListener("message", connect);
    const port = event.ports[0];
    let busy = false;
    port.onmessage = async ({data}) => {
      if (busy) {
        port.postMessage({ seq: data.seq, error: "前一个下载仍在进行，请稍后重试。" });
        return;
      }
      busy = true;
      try { port.postMessage({ seq: data.seq, value: await respond(data.action, data.id) }); }
      catch (e) { port.postMessage({ seq: data.seq, error: e.message || "取题失败。" }); }
      finally { busy = false; }
    };
    port.postMessage({ ready: true });
  }
  window.addEventListener("message", connect);
  setTimeout(() => window.removeEventListener("message", connect), 120000);
}
