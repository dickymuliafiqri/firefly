/**
 * AudioManager.ts — Procedural Lofi Synthesizer (Web Audio API)
 * Zero external MP3 assets, 64 BPM nocturnal lofi jazz voicings.
 * Compliant with browser autoplay policies.
 */

// Hoisted module-level static musical structures (Vercel Best Practice: server-hoist-static-io)
export interface ChordDefinition {
  bass: number;
  notes: number[];
}

const LOFI_CHORDS: readonly ChordDefinition[] = [
  // Bar 0: Dm9 (D3, F3, A3, C4, E4)
  { bass: 73.42, notes: [146.83, 174.61, 220.00, 261.63, 329.63] },
  // Bar 1: G13 (G2, F3, B3, E4, A4)
  { bass: 49.00, notes: [98.00, 174.61, 246.94, 329.63, 440.00] },
  // Bar 2: Cmaj9 (C3, E3, G3, B3, D4)
  { bass: 65.41, notes: [130.81, 164.81, 196.00, 246.94, 293.66] },
  // Bar 3: Am9 (A2, C3, E3, G3, B3, E4)
  { bass: 55.00, notes: [110.00, 164.81, 196.00, 246.94, 329.63] },
];

const SPARKLE_FREQUENCIES: readonly number[] = [
  659.25, 783.99, 880.00, 987.77, 1046.50, 1174.66, 1318.51,
];

type AudioStateListener = (isPlaying: boolean) => void;

export class AudioManager {
  private static instance: AudioManager | null = null;
  private ctx: AudioContext | null = null;
  private isPlaying = false;
  private timerID: ReturnType<typeof setTimeout> | null = null;
  public readonly tempo = 64; // 64 BPM
  private readonly beatDuration = 60 / 64; // ~0.9375s per beat
  private step = 0; // 64 16th steps in 4-bar loop
  private nextNoteTime = 0;
  private masterGain: GainNode | null = null;
  private filter: BiquadFilterNode | null = null;
  private listeners: Set<AudioStateListener> = new Set();

  private constructor() {}

  public static getInstance(): AudioManager {
    if (!AudioManager.instance) {
      AudioManager.instance = new AudioManager();
    }
    return AudioManager.instance;
  }

  public getIsPlaying(): boolean {
    return this.isPlaying;
  }

  public subscribe(listener: AudioStateListener): () => void {
    this.listeners.add(listener);
    listener(this.isPlaying);
    return () => {
      this.listeners.delete(listener);
    };
  }

  private notify() {
    for (const listener of this.listeners) {
      listener(this.isPlaying);
    }
  }

  private init() {
    if (this.ctx) return;
    const AudioCtxClass = window.AudioContext || (window as unknown as { webkitAudioContext: typeof AudioContext }).webkitAudioContext;
    this.ctx = new AudioCtxClass();

    // Master output bus
    this.masterGain = this.ctx.createGain();
    this.masterGain.gain.setValueAtTime(0.0001, this.ctx.currentTime);

    // Warm analog lowpass filter to soften highs
    this.filter = this.ctx.createBiquadFilter();
    this.filter.type = 'lowpass';
    this.filter.frequency.setValueAtTime(2600, this.ctx.currentTime);
    this.filter.Q.setValueAtTime(0.7, this.ctx.currentTime);

    this.masterGain.connect(this.filter);
    this.filter.connect(this.ctx.destination);

    // Vinyl and tape crackle texture
    this.initVinylNoise();
  }

  private initVinylNoise() {
    if (!this.ctx || !this.masterGain) return;

    const bufferSize = this.ctx.sampleRate * 3; // 3-second loop
    const noiseBuffer = this.ctx.createBuffer(1, bufferSize, this.ctx.sampleRate);
    const output = noiseBuffer.getChannelData(0);
    let lastOut = 0.0;

    for (let i = 0; i < bufferSize; i++) {
      const white = Math.random() * 2 - 1;
      output[i] = (lastOut + 0.02 * white) / 1.02; // Brown noise filter
      lastOut = output[i];
      // Gentle vinyl crackle
      if (Math.random() < 0.00025) {
        output[i] += (Math.random() * 2 - 1) * 0.15;
      }
    }

    const noiseSource = this.ctx.createBufferSource();
    noiseSource.buffer = noiseBuffer;
    noiseSource.loop = true;

    const noiseFilter = this.ctx.createBiquadFilter();
    noiseFilter.type = 'bandpass';
    noiseFilter.frequency.setValueAtTime(800, this.ctx.currentTime);
    noiseFilter.Q.setValueAtTime(0.8, this.ctx.currentTime);

    const noiseGain = this.ctx.createGain();
    noiseGain.gain.setValueAtTime(0.028, this.ctx.currentTime);

    noiseSource.connect(noiseFilter);
    noiseFilter.connect(noiseGain);
    noiseGain.connect(this.masterGain);

    noiseSource.start();
  }

  // Warm lofi Rhodes electric piano voice with subtle wow & flutter
  private playRhodesNote(freq: number, time: number, duration = 2.4, velocity = 0.14) {
    if (!this.ctx || !this.masterGain) return;

    const osc1 = this.ctx.createOscillator();
    const osc2 = this.ctx.createOscillator();
    osc1.type = 'sine';
    osc2.type = 'triangle';

    osc1.frequency.setValueAtTime(freq, time);
    osc2.frequency.setValueAtTime(freq * 1.002, time);

    // Analog tape flutter
    const lfo = this.ctx.createOscillator();
    const lfoGain = this.ctx.createGain();
    lfo.frequency.setValueAtTime(0.35, time);
    lfoGain.gain.setValueAtTime(2.2, time);
    lfo.connect(lfoGain);
    lfoGain.connect(osc1.frequency);
    lfoGain.connect(osc2.frequency);

    const noteFilter = this.ctx.createBiquadFilter();
    noteFilter.type = 'lowpass';
    noteFilter.frequency.setValueAtTime(650, time);
    noteFilter.Q.setValueAtTime(1.2, time);

    const noteGain = this.ctx.createGain();
    noteGain.gain.setValueAtTime(0.0001, time);
    noteGain.gain.linearRampToValueAtTime(velocity, time + 0.06);
    noteGain.gain.exponentialRampToValueAtTime(velocity * 0.45, time + 0.9);
    noteGain.gain.exponentialRampToValueAtTime(0.0001, time + duration);

    osc1.connect(noteFilter);
    osc2.connect(noteFilter);
    noteFilter.connect(noteGain);
    noteGain.connect(this.masterGain);

    lfo.start(time);
    osc1.start(time);
    osc2.start(time);

    lfo.stop(time + duration);
    osc1.stop(time + duration);
    osc2.stop(time + duration);
  }

  // Deep soothing sub-bass
  private playBassNote(freq: number, time: number, duration = 1.8) {
    if (!this.ctx || !this.masterGain) return;
    const osc = this.ctx.createOscillator();
    osc.type = 'sine';
    osc.frequency.setValueAtTime(freq, time);

    const bassGain = this.ctx.createGain();
    bassGain.gain.setValueAtTime(0.0001, time);
    bassGain.gain.linearRampToValueAtTime(0.18, time + 0.08);
    bassGain.gain.exponentialRampToValueAtTime(0.0001, time + duration);

    osc.connect(bassGain);
    bassGain.connect(this.masterGain);

    osc.start(time);
    osc.stop(time + duration);
  }

  // Star chime twinkle
  private playChimeNote(freq: number, time: number) {
    if (!this.ctx || !this.masterGain) return;
    const osc = this.ctx.createOscillator();
    osc.type = 'sine';
    osc.frequency.setValueAtTime(freq, time);

    const gain = this.ctx.createGain();
    gain.gain.setValueAtTime(0.0001, time);
    gain.gain.linearRampToValueAtTime(0.045, time + 0.01);
    gain.gain.exponentialRampToValueAtTime(0.0001, time + 2.2);

    osc.connect(gain);
    gain.connect(this.masterGain);

    osc.start(time);
    osc.stop(time + 2.2);
  }

  // Soft muted kick
  private playKick(time: number) {
    if (!this.ctx || !this.masterGain) return;
    const osc = this.ctx.createOscillator();
    osc.type = 'sine';
    osc.frequency.setValueAtTime(110, time);
    osc.frequency.exponentialRampToValueAtTime(42, time + 0.12);

    const gain = this.ctx.createGain();
    gain.gain.setValueAtTime(0.16, time);
    gain.gain.exponentialRampToValueAtTime(0.001, time + 0.14);

    osc.connect(gain);
    gain.connect(this.masterGain);

    osc.start(time);
    osc.stop(time + 0.15);
  }

  // Gentle snare brush
  private playSnare(time: number) {
    if (!this.ctx || !this.masterGain) return;
    const bufferSize = Math.floor(this.ctx.sampleRate * 0.08);
    const buffer = this.ctx.createBuffer(1, bufferSize, this.ctx.sampleRate);
    const data = buffer.getChannelData(0);
    for (let i = 0; i < bufferSize; i++) {
      data[i] = (Math.random() * 2 - 1) * Math.exp(-i / (bufferSize * 0.3));
    }

    const noise = this.ctx.createBufferSource();
    noise.buffer = buffer;

    const filter = this.ctx.createBiquadFilter();
    filter.type = 'lowpass';
    filter.frequency.setValueAtTime(1100, time);

    const gain = this.ctx.createGain();
    gain.gain.setValueAtTime(0.07, time);
    gain.gain.exponentialRampToValueAtTime(0.001, time + 0.08);

    noise.connect(filter);
    filter.connect(gain);
    gain.connect(this.masterGain);

    noise.start(time);
    noise.stop(time + 0.08);
  }

  private scheduleStep(step: number, time: number) {
    const beatInBar = Math.floor(step / 4);
    const sixteenth = step % 4;
    const bar = Math.floor(step / 16) % 4;
    const currentChord = LOFI_CHORDS[bar];

    // Downbeat chord strum on step 0
    if (step % 16 === 0) {
      this.playBassNote(currentChord.bass, time, this.beatDuration * 3.6);
      currentChord.notes.forEach((freq, idx) => {
        const strumOffset = idx * 0.022;
        this.playRhodesNote(freq, time + strumOffset, this.beatDuration * 3.8, 0.12);
      });
    } else if (step % 16 === 8) {
      currentChord.notes.slice(1).forEach((freq, idx) => {
        const strumOffset = idx * 0.018;
        this.playRhodesNote(freq, time + strumOffset, this.beatDuration * 1.8, 0.07);
      });
    }

    // Groove
    if (sixteenth === 0) {
      if (beatInBar === 0 || beatInBar === 2) {
        this.playKick(time);
      }
      if (beatInBar === 1 || beatInBar === 3) {
        this.playSnare(time);
      }
    }

    // Sparkles
    if (Math.random() < 0.18) {
      const sparkleFreq = SPARKLE_FREQUENCIES[Math.floor(Math.random() * SPARKLE_FREQUENCIES.length)];
      this.playChimeNote(sparkleFreq, time + Math.random() * 0.1);
    }
  }

  private scheduler() {
    if (!this.ctx) return;
    const lookahead = 0.1;
    const stepTime = this.beatDuration / 4;

    while (this.nextNoteTime < this.ctx.currentTime + lookahead) {
      this.scheduleStep(this.step, this.nextNoteTime);
      this.step = (this.step + 1) % 64;
      this.nextNoteTime += stepTime;
    }

    if (this.isPlaying) {
      this.timerID = setTimeout(() => this.scheduler(), 35);
    }
  }

  public async start(): Promise<void> {
    this.init();
    if (!this.ctx || !this.masterGain) return;

    if (this.ctx.state === 'suspended') {
      await this.ctx.resume();
    }

    this.isPlaying = true;
    this.step = 0;
    this.nextNoteTime = this.ctx.currentTime + 0.05;

    // Smooth fade-in
    this.masterGain.gain.cancelScheduledValues(this.ctx.currentTime);
    this.masterGain.gain.setValueAtTime(this.masterGain.gain.value || 0.0001, this.ctx.currentTime);
    this.masterGain.gain.linearRampToValueAtTime(0.24, this.ctx.currentTime + 1.2);

    this.scheduler();
    this.notify();
  }

  public stop(): void {
    if (!this.isPlaying || !this.ctx || !this.masterGain) return;
    this.isPlaying = false;
    if (this.timerID) {
      clearTimeout(this.timerID);
      this.timerID = null;
    }

    // Smooth fade-out
    this.masterGain.gain.cancelScheduledValues(this.ctx.currentTime);
    this.masterGain.gain.setValueAtTime(this.masterGain.gain.value, this.ctx.currentTime);
    this.masterGain.gain.linearRampToValueAtTime(0.0001, this.ctx.currentTime + 0.8);

    setTimeout(() => {
      if (!this.isPlaying && this.ctx && this.ctx.state === 'running') {
        this.ctx.suspend();
      }
    }, 850);

    this.notify();
  }

  public toggle(): void {
    if (this.isPlaying) {
      this.stop();
    } else {
      this.start().catch((err) => console.error('Audio start failed', err));
    }
  }
}
