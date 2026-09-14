"""Worker 构建/能力标识，随源码走，用于运行时确认进程是否加载了新代码。

旧进程的 /health、/metrics 不返回 version 字段，可据此判断：
    curl -s localhost:8000/health | grep version
"""

# 静态版本号：每次发布 worker 能力变更时递增。
# 2026.09.14-vad：自适应 VAD，修复低响度说话空转写。
VERSION = "2026.09.14-vad"
