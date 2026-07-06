---
title: "Voice"
description: "Voice both ways, all local so no audio leaves the machine. Transcribe inbound voice notes with whisper.cpp, speak replies back with piper, and keep it reciprocal: talk and it talks back, type and it stays text."
weight: 60
---

tomo can listen and speak, and it does both on your machine.
Inbound voice notes are transcribed with whisper.cpp; replies are spoken back with piper.
Both run by shelling out to local binaries, so no audio is ever sent to a cloud speech API.
There is nothing to key and nothing leaves the box.

Voice is off until you set a model, and the two directions are independent: turn on transcription, spoken replies, or both.

## Inbound: transcription

Set `voice.model` to a ggml whisper model and tomo transcribes voice notes with whisper.cpp.
The transcript folds into the turn exactly like typed text, so a voice note and a typed message reach the model the same way.
tomo posts a short notice of what it heard, and a note that fails to transcribe is dropped rather than aborting the turn.

Under the hood, ffmpeg decodes the incoming clip to 16 kHz mono WAV, then the whisper binary transcribes it.

## Outbound: spoken replies

Set `voice.tts_model` to a piper voice model and tomo speaks its replies back as a voice note.
piper renders the text to WAV and ffmpeg encodes it to Opus, the container messaging apps treat as a real voice note.

This is reciprocal on purpose.
A spoken reply goes back only when you spoke first, and it is sent wherever you spoke.
Talk to tomo and it talks back; type and it stays text.
Code fences are stripped before speaking, since a wall of code makes a miserable voice note, and a long answer is capped so it does not turn into a long recording.
Spoken replies also depend on the channel being able to carry audio out.

## Configuration

```yaml
voice:
  model: ~/.tomo/models/ggml-base.en.bin
  bin: whisper-cli
  ffmpeg: ffmpeg
  tts_model: ~/.tomo/models/en_US-amy-medium.onnx
  tts_bin: piper
```

- `model` is the path to a ggml whisper model; setting it enables transcription.
- `tts_model` is the path to a piper voice model; setting it enables spoken replies.
- `bin` is the whisper.cpp cli, defaulting to `whisper-cli` on your PATH.
- `tts_bin` is the piper cli, defaulting to `piper` on your PATH.
- `ffmpeg` is used to decode inbound clips and encode the spoken reply, defaulting to `ffmpeg` on your PATH.

The binaries default to `whisper-cli`, `piper`, and `ffmpeg` on your PATH and are never bundled, so install them yourself and point the config at them if they live somewhere unusual.
Set only `model` for listen-only, only `tts_model` for speak-only, or both for a full voice conversation.
