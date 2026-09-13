/**
 * 浏览器音频采集器：
 *  getUserMedia -> AudioContext(16kHz) -> AudioWorklet(PCM S16LE 固定切片)
 *  不支持 AudioWorklet 时回退 ScriptProcessorNode。
 *
 * 每片输出: { seq, startMs, endMs, sampleRate, pcm: ArrayBuffer(S16LE) }
 */
import pcmWorkletUrl from "../worklets/pcm-chunk-processor.js?url";

export interface PcmChunk {
  seq: number;
  startMs: number;
  endMs: number;
  sampleRate: number;
  channels: number;
  pcm: ArrayBuffer;
}

export type ChunkHandler = (chunk: PcmChunk) => void | Promise<void>;

const TARGET_SAMPLE_RATE = 16000;
const CHUNK_SECONDS = 3;

export class AudioRecorder {
  private ctx: AudioContext | null = null;
  private node: AudioWorkletNode | ScriptProcessorNode | null = null;
  private stream: MediaStream | null = null;
  private mediaSource: MediaStreamAudioSourceNode | null = null;
  private fallbackBuffer: Float32Array[] = [];
  private fallbackSamples = 0;
  private fallbackSeq = 0;
  private fallbackAnchor = 0;

  async start(onChunk: ChunkHandler): Promise<void> {
    this.stream = await navigator.mediaDevices.getUserMedia({
      audio: {
        channelCount: 1,
        echoCancellation: true,
        noiseSuppression: true,
        autoGainControl: true,
      },
    });

    const AudioContextCtor =
      window.AudioContext ??
      (window as unknown as { webkitAudioContext: typeof AudioContext }).webkitAudioContext;
    this.ctx = new AudioContextCtor({ sampleRate: TARGET_SAMPLE_RATE });
    if (this.ctx.state === "suspended") {
      await this.ctx.resume();
    }

    const actualRate = this.ctx.sampleRate;

    if (this.ctx.audioWorklet) {
      await this.ctx.audioWorklet.addModule(pcmWorkletUrl);
      const worklet = new AudioWorkletNode(this.ctx, "pcm-chunk-processor", {
        numberOfInputs: 1,
        numberOfOutputs: 0,
        processorOptions: { chunkSeconds: CHUNK_SECONDS },
      });
      worklet.port.onmessage = (event: MessageEvent) => {
        const data = event.data as PcmChunk;
        if (actualRate === TARGET_SAMPLE_RATE) {
          onChunk(data);
        } else {
          onChunk(this._resampleChunk(data, actualRate, TARGET_SAMPLE_RATE));
        }
      };
      worklet.parameters.get("chunkSeconds")?.setValueAtTime(CHUNK_SECONDS, 0);
      this.mediaSource = this.ctx.createMediaStreamSource(this.stream);
      this.mediaSource.connect(worklet);
      this.node = worklet;
    } else {
      this._startScriptProcessor(onChunk);
    }
  }

  stop(): void {
    this.mediaSource?.disconnect();
    this.node?.disconnect();
    this.stream?.getTracks().forEach((track) => track.stop());
    void this.ctx?.close();
    this.ctx = null;
    this.node = null;
    this.stream = null;
    this.mediaSource = null;
    this.fallbackBuffer = [];
    this.fallbackSamples = 0;
  }

  get running(): boolean {
    return this.ctx?.state === "running";
  }

  private _startScriptProcessor(onChunk: ChunkHandler) {
    const ctx = this.ctx!;
    const rate = ctx.sampleRate;
    const processor = ctx.createScriptProcessor(4096, 1, 1);
    this.mediaSource = ctx.createMediaStreamSource(this.stream!);
    this.mediaSource.connect(processor);
    // ScriptProcessor 需要连接 destination 才会持续触发（不发声）。
    processor.connect(ctx.destination);

    processor.onaudioprocess = (event) => {
      const input = event.inputBuffer.getChannelData(0);
      if (this.fallbackSamples === 0) {
        this.fallbackAnchor = Date.now();
      }
      this.fallbackBuffer.push(new Float32Array(input));
      this.fallbackSamples += input.length;

      const target = Math.round(rate * CHUNK_SECONDS);
      if (this.fallbackSamples >= target) {
        const merged = new Float32Array(this.fallbackSamples);
        let offset = 0;
        for (const part of this.fallbackBuffer) {
          merged.set(part, offset);
          offset += part.length;
        }
        const slice = merged.subarray(0, target);
        const pcm16 = floatTo16(slice);
        const startMs = this.fallbackAnchor;
        const endMs = startMs + Math.round((pcm16.length / rate) * 1000);
        const chunk: PcmChunk = {
          seq: this.fallbackSeq++,
          startMs,
          endMs,
          sampleRate: rate,
          channels: 1,
          pcm: pcm16.buffer as ArrayBuffer,
        };
        onChunk(rate === TARGET_SAMPLE_RATE ? chunk : this._resampleChunk(chunk, rate, TARGET_SAMPLE_RATE));
        this.fallbackBuffer = [];
        this.fallbackSamples = 0;
      }
    };
    this.node = processor;
  }

  private _resampleChunk(chunk: PcmChunk, fromRate: number, toRate: number): PcmChunk {
    const input = new Int16Array(chunk.pcm);
    const float = new Float32Array(input.length);
    for (let i = 0; i < input.length; i++) {
      float[i] = input[i] / 32768;
    }
    const resampled = linearResample(float, fromRate, toRate);
    const out16 = floatTo16(resampled);
    return {
      ...chunk,
      sampleRate: toRate,
      pcm: out16.buffer as ArrayBuffer,
      endMs: chunk.startMs + Math.round((out16.length / toRate) * 1000),
    };
  }
}

export function floatTo16(input: Float32Array): Int16Array {
  const out = new Int16Array(input.length);
  for (let i = 0; i < input.length; i++) {
    const s = Math.max(-1, Math.min(1, input[i]));
    out[i] = s < 0 ? s * 0x8000 : s * 0x7fff;
  }
  return out;
}

function linearResample(input: Float32Array, fromRate: number, toRate: number): Float32Array {
  if (fromRate === toRate) return input;
  const ratio = fromRate / toRate;
  const newLength = Math.round(input.length / ratio);
  const out = new Float32Array(newLength);
  for (let i = 0; i < newLength; i++) {
    const pos = i * ratio;
    const index = Math.floor(pos);
    const fraction = pos - index;
    const a = input[Math.min(index, input.length - 1)];
    const b = input[Math.min(index + 1, input.length - 1)];
    out[i] = a + (b - a) * fraction;
  }
  return out;
}
