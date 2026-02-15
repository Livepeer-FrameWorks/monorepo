import { HITMARKER_AUDIO_DATA_URL } from "../../constants/media-assets.js";

function createSyntheticHitmarkerSound(): void {
  try {
    const AudioContextCtor =
      window.AudioContext ||
      ((window as unknown as { webkitAudioContext?: typeof AudioContext }).webkitAudioContext as
        | typeof AudioContext
        | undefined);
    if (!AudioContextCtor) {
      return;
    }

    const audioContext = new AudioContextCtor();

    const oscillator1 = audioContext.createOscillator();
    const oscillator2 = audioContext.createOscillator();
    const noiseBuffer = audioContext.createBuffer(
      1,
      audioContext.sampleRate * 0.1,
      audioContext.sampleRate
    );
    const noiseSource = audioContext.createBufferSource();

    const noiseData = noiseBuffer.getChannelData(0);
    for (let index = 0; index < noiseData.length; index += 1) {
      noiseData[index] = Math.random() * 2 - 1;
    }
    noiseSource.buffer = noiseBuffer;

    const gainNode1 = audioContext.createGain();
    const gainNode2 = audioContext.createGain();
    const noiseGain = audioContext.createGain();
    const masterGain = audioContext.createGain();

    oscillator1.connect(gainNode1);
    oscillator2.connect(gainNode2);
    noiseSource.connect(noiseGain);

    gainNode1.connect(masterGain);
    gainNode2.connect(masterGain);
    noiseGain.connect(masterGain);
    masterGain.connect(audioContext.destination);

    oscillator1.frequency.setValueAtTime(1800, audioContext.currentTime);
    oscillator1.frequency.exponentialRampToValueAtTime(900, audioContext.currentTime + 0.08);
    oscillator2.frequency.setValueAtTime(3600, audioContext.currentTime);
    oscillator2.frequency.exponentialRampToValueAtTime(1800, audioContext.currentTime + 0.04);

    oscillator1.type = "triangle";
    oscillator2.type = "sine";

    gainNode1.gain.setValueAtTime(0, audioContext.currentTime);
    gainNode1.gain.linearRampToValueAtTime(0.4, audioContext.currentTime + 0.002);
    gainNode1.gain.exponentialRampToValueAtTime(0.001, audioContext.currentTime + 0.12);

    gainNode2.gain.setValueAtTime(0, audioContext.currentTime);
    gainNode2.gain.linearRampToValueAtTime(0.3, audioContext.currentTime + 0.001);
    gainNode2.gain.exponentialRampToValueAtTime(0.001, audioContext.currentTime + 0.06);

    noiseGain.gain.setValueAtTime(0, audioContext.currentTime);
    noiseGain.gain.linearRampToValueAtTime(0.2, audioContext.currentTime + 0.001);
    noiseGain.gain.exponentialRampToValueAtTime(0.001, audioContext.currentTime + 0.01);

    masterGain.gain.setValueAtTime(0.5, audioContext.currentTime);

    const startTime = audioContext.currentTime;
    const stopTime = startTime + 0.15;

    oscillator1.start(startTime);
    oscillator2.start(startTime);
    noiseSource.start(startTime);

    oscillator1.stop(stopTime);
    oscillator2.stop(stopTime);
    noiseSource.stop(startTime + 0.02);
  } catch {
    // Ignore audio errors.
  }
}

export function playHitmarkerSound(): void {
  try {
    const audio = new Audio(HITMARKER_AUDIO_DATA_URL);
    audio.volume = 0.3;
    void audio.play().catch(() => {
      createSyntheticHitmarkerSound();
    });
  } catch {
    createSyntheticHitmarkerSound();
  }
}
