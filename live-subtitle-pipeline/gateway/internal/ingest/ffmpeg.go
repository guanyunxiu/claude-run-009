package ingest

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// Puller 抽象“从源拉到 PCM 流”的过程，便于测试替换。
type Puller interface {
	// Start 返回 ffmpeg 的 stdout（S16LE PCM）流；失败返回错误。
	Start(ctx context.Context) (io.ReadCloser, error)
	// Wait 等待进程退出，返回退出错误（nil 表示正常）。
	Wait() error
	// PID 返回进程 PID（未启动为 0）。
	PID() int
	// Kill 终止进程。
	Kill() error
}

// FFmpegPuller 用系统 ffmpeg 把 RTMP/HLS 拉成 16kHz mono S16LE。
type FFmpegPuller struct {
	source string
	kind   SourceKind
	cmd    *exec.Cmd
}

func NewFFmpegPuller(source string, kind SourceKind) *FFmpegPuller {
	return &FFmpegPuller{source: source, kind: kind}
}

// Args 暴露命令参数（测试用，便于校验重采样/容器处理）。
func (p *FFmpegPuller) Args() []string {
	args := []string{
		"-hide_banner", "-loglevel", "error",
		"-fflags", "+genpts",
	}
	if p.kind == KindHLS {
		// HLS 直播列表会持续更新。
		args = append(args, "-re")
	}
	args = append(args,
		"-i", p.source,
		"-vn",
		"-ac", "1",
		"-ar", "16000",
		"-f", "s16le",
		"-acodec", "pcm_s16le",
		"pipe:1",
	)
	return args
}

func (p *FFmpegPuller) Start(ctx context.Context) (io.ReadCloser, error) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return nil, fmt.Errorf("ffmpeg not found in PATH: %w", err)
	}
	p.cmd = exec.CommandContext(ctx, "ffmpeg", p.Args()...)
	stdout, err := p.cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr strings.Builder
	p.cmd.Stderr = &stderr
	if err := p.cmd.Start(); err != nil {
		return nil, fmt.Errorf("ffmpeg start: %w (%s)", err, stderr.String())
	}
	return stdout, nil
}

func (p *FFmpegPuller) Wait() error {
	if p.cmd == nil || p.cmd.Process == nil {
		return nil
	}
	return p.cmd.Wait()
}

func (p *FFmpegPuller) PID() int {
	if p.cmd != nil && p.cmd.Process != nil {
		return p.cmd.Process.Pid
	}
	return 0
}

func (p *FFmpegPuller) Kill() error {
	if p.cmd != nil && p.cmd.Process != nil {
		return p.cmd.Process.Kill()
	}
	return nil
}

// PullerFactory 便于在 Manager 中注入测试 Puller。
type PullerFactory func(source string, kind SourceKind) Puller

func defaultPullerFactory(source string, kind SourceKind) Puller {
	return NewFFmpegPuller(source, kind)
}
