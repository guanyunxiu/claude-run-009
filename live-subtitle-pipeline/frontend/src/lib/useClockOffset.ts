import { useEffect, useState } from "react";
import { api } from "./api";

/**
 * 客户端/服务端时钟偏差估算（毫秒）：offset = serverMs - localMs。
 * 用多次采样取最小往返延迟的那一次，降低网络抖动影响。
 */
export function useClockOffset(): number {
  const [offset, setOffset] = useState(0);

  useEffect(() => {
    let cancelled = false;
    const samples: { rtt: number; offset: number }[] = [];

    const sample = async () => {
      const t0 = Date.now();
      const time = await api.serverTime();
      const t1 = Date.now();
      const rtt = t1 - t0;
      const serverNow = time.serverMs + rtt / 2; // 把服务端时刻推算到响应到达时
      const sampleOffset = serverNow - t1;
      samples.push({ rtt, offset: sampleOffset });
      if (samples.length >= 3) {
        samples.sort((a, b) => a.rtt - b.rtt);
        if (!cancelled) setOffset(samples[0].offset);
        return true;
      }
      return false;
    };

    void (async () => {
      for (let i = 0; i < 3; i++) {
        try {
          if (await sample()) break;
        } catch {
          /* 时钟同步失败时退化为 offset=0，不阻塞主流程 */
          break;
        }
      }
    })();

    return () => {
      cancelled = true;
    };
  }, []);

  return offset;
}
