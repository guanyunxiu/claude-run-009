import { useEffect, useState } from "react";
import { buildViewerLink } from "../lib/tokens";

interface Props {
  sessionId: string;
  viewToken?: string;
}

/** 主播台：可一键复制的观众邀请链接（含 viewToken）。 */
export function InviteLink({ sessionId, viewToken }: Props) {
  const [copied, setCopied] = useState(false);
  const [link, setLink] = useState("");

  useEffect(() => {
    if (viewToken) {
      setLink(buildViewerLink(sessionId, viewToken));
    } else {
      // 兜底：从本机 localStorage 取 viewToken。
      try {
        const tok = localStorage.getItem(`lsp:view:${sessionId}`) ?? "";
        setLink(tok ? buildViewerLink(sessionId, tok) : "");
      } catch {
        setLink("");
      }
    }
  }, [sessionId, viewToken]);

  if (!link) return null;

  async function copy() {
    try {
      await navigator.clipboard.writeText(link);
    } catch {
      // 非安全上下文（http 非 localhost）时退化为选中输入框。
      const input = document.getElementById("invite-link-input") as HTMLInputElement | null;
      input?.select();
      document.execCommand("copy");
    }
    setCopied(true);
    window.setTimeout(() => setCopied(false), 2000);
  }

  return (
    <div className="flex w-full max-w-md items-center gap-2">
      <input
        id="invite-link-input"
        readOnly
        value={link}
        onFocus={(e) => e.target.select()}
        className="min-w-0 flex-1 truncate rounded-lg border border-slate-700 bg-slate-950 px-3 py-2 text-xs text-slate-300"
      />
      <button
        type="button"
        onClick={copy}
        className="shrink-0 rounded-lg border border-slate-600 px-3 py-2 text-xs font-semibold text-slate-200 hover:border-slate-400 hover:bg-slate-800"
      >
        {copied ? "已复制 ✓" : "复制邀请链接"}
      </button>
    </div>
  );
}
