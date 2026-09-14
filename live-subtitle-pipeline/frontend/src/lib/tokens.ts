/**
 * 会话鉴权令牌管理。
 *
 * 创建会话时网关一次性返回 hostToken（主播）与 viewToken（观众）。
 * 浏览器用 localStorage 持久化：
 *   lsp:host:<sessionId> = hostToken   主播台用（上传/结束/看字幕）
 *   lsp:view:<sessionId> = viewToken   观众席用（看字幕/挂 WS）
 *
 * 观众通过主播分享的、URL 上带 ?token=<viewToken> 的链接进入。
 */

const HOST_PREFIX = "lsp:host:";
const VIEW_PREFIX = "lsp:view:";

export type Role = "host" | "view";

export function saveTokens(sessionId: string, tokens: { hostToken: string; viewToken: string }) {
  try {
    localStorage.setItem(HOST_PREFIX + sessionId, tokens.hostToken);
    localStorage.setItem(VIEW_PREFIX + sessionId, tokens.viewToken);
  } catch {
    /* 隐私模式等场景 localStorage 不可用时退化为内存态 */
  }
}

export function saveViewToken(sessionId: string, token: string) {
  try {
    localStorage.setItem(VIEW_PREFIX + sessionId, token);
  } catch {
    /* ignore */
  }
}

export function getHostToken(sessionId: string): string {
  try {
    return localStorage.getItem(HOST_PREFIX + sessionId) ?? "";
  } catch {
    return "";
  }
}

export function getViewToken(sessionId: string): string {
  try {
    return localStorage.getItem(VIEW_PREFIX + sessionId) ?? "";
  } catch {
    return "";
  }
}

/**
 * 取用于本会话的访问令牌：优先 URL query（分享链接），其次本地存储。
 * host 页面用 hostToken，view 页面优先 viewToken（也接受 hostToken）。
 */
export function resolveToken(
  sessionId: string,
  role: Role,
  queryToken?: string | null,
): string {
  if (queryToken) {
    // 观众通过分享链接进入：固化 viewToken 以便刷新后仍可用。
    if (role === "view") saveViewToken(sessionId, queryToken);
    return queryToken;
  }
  if (role === "host") {
    return getHostToken(sessionId) || getViewToken(sessionId);
  }
  return getViewToken(sessionId) || getHostToken(sessionId);
}

export function buildViewerLink(sessionId: string, viewToken: string): string {
  const url = new URL(`/watch/${sessionId}`, window.location.origin);
  url.searchParams.set("token", viewToken);
  return url.toString();
}

/**
 * 构造站内 <Link to=…> 用的观众路径，确保总是带 token。
 * 优先用显式传入的令牌，否则回退到本机已保存的 viewToken/hostToken；
 * 都没有时返回 null（调用方应隐藏入口，而不是渲染一个必然 401 的裸链接）。
 */
export function viewerPath(sessionId: string, explicitToken?: string): string | null {
  const token = explicitToken || getViewToken(sessionId) || getHostToken(sessionId);
  if (!token) return null;
  return `/watch/${sessionId}?token=${encodeURIComponent(token)}`;
}
