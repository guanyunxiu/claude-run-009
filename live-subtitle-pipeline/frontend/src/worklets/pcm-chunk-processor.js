// PCM 采集 AudioWorklet：把每帧 Float32 样本累积到固定时长（默认 3s）后
// 以 S16LE ArrayBuffer 回传主线程。
/* global registerProcessor */

class PcmChunkProcessor extends AudioWorkletProcessor {
  static get parameterDescriptors() {
    return [
      {
        name: "chunkSeconds",
        defaultValue: 3,
        minValue: 1,
        maxValue: 10,
        automationRate: "k-rate",
      },
    ];
  }

  constructor() {
    super();
    this.buffers = [];
    this.bufferedSamples = 0;
    this.targetSamples = 48000; // process() 中按采样率修正
    this.sampleRate = 48000;
    this.chunkSeq = 0;
    this.anchorMs = 0;
  }

  process(inputs, _outputs, parameters) {
    const input = inputs[0];
    if (!input || input.length === 0) {
      return true;
    }
    // 所有输入声道下混为单声道。
    const channels = input.length;
    const frame = new Float32Array(input[0].length);
    for (let i = 0; i < frame.length; i++) {
      let sum = 0;
      for (let c = 0; c < channels; c++) {
        sum += input[c][i];
      }
      frame[i] = sum / channels;
    }

    const chunkSeconds = parameters.chunkSeconds[0];
    this.sampleRate = globalThis.sampleRate || this.sampleRate;
    this.targetSamples = Math.round(this.sampleRate * chunkSeconds);

    if (this.bufferedSamples === 0) {
      this.anchorMs = Date.now();
    }

    this.buffers.push(frame);
    this.bufferedSamples += frame.length;

    if (this.bufferedSamples >= this.targetSamples) {
      const merged = this._merge();
      const pcm16 = PcmChunkProcessor._floatTo16(merged);
      const startMs = this.anchorMs;
      const endMs = startMs + Math.round((pcm16.byteLength / 2 / this.sampleRate) * 1000);
      this.port.postMessage(
        {
          type: "chunk",
          seq: this.chunkSeq++,
          startMs,
          endMs,
          sampleRate: this.sampleRate,
          channels: 1,
          pcm: pcm16.buffer,
        },
        [pcm16.buffer],
      );
      this.buffers = [];
      this.bufferedSamples = 0;
    }
    return true;
  }

  _merge() {
    const merged = new Float32Array(this.bufferedSamples);
    let offset = 0;
    for (const buffer of this.buffers) {
      merged.set(buffer, offset);
      offset += buffer.length;
    }
    // 取整到目标长度，丢弃跨片的少量样本（保持固定切片节奏）。
    return merged.subarray(0, this.targetSamples);
  }

  static _floatTo16(float32) {
    const out = new Int16Array(float32.length);
    for (let i = 0; i < float32.length; i++) {
      const s = Math.max(-1, Math.min(1, float32[i]));
      out[i] = s < 0 ? s * 0x8000 : s * 0x7fff;
    }
    return out;
  }
}

registerProcessor("pcm-chunk-processor", PcmChunkProcessor);
