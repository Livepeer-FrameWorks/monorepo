// Multistream destinations the push-target dialog offers. A preset fixes the
// ingest URL and asks only for the stream key; keyHelp is the platform page
// that shows the key, the same links builders/multistreaming.mdx lists.

export interface PushTargetPreset {
  label: string;
  ingestUrl: string;
  placeholder: string;
  keyHelp?: { label: string; url: string };
}

export const PUSH_TARGET_PRESETS: Record<string, PushTargetPreset> = {
  twitch: {
    label: "Twitch",
    ingestUrl: "rtmp://live.twitch.tv/app/",
    placeholder: "live_xxxxxxxxxxxx",
    keyHelp: {
      label: "Twitch Creator Dashboard, Settings, Stream",
      url: "https://dashboard.twitch.tv/settings/stream",
    },
  },
  youtube: {
    label: "YouTube",
    ingestUrl: "rtmp://a.rtmp.youtube.com/live2/",
    placeholder: "xxxx-xxxx-xxxx-xxxx",
    keyHelp: {
      label: "YouTube Live Control Room",
      url: "https://support.google.com/youtube/answer/2907883",
    },
  },
  facebook: {
    label: "Facebook",
    ingestUrl: "rtmps://live-api-s.facebook.com:443/rtmp/",
    placeholder: "FB-xxxx...",
    keyHelp: {
      label: "Facebook Live Producer, Streaming software",
      url: "https://www.facebook.com/help/587160588142067",
    },
  },
  kick: {
    label: "Kick",
    ingestUrl: "rtmps://fa723fc1b171.global-contribute.live-video.net/app/",
    placeholder: "sk_xxxxxxxxxxxx",
    keyHelp: {
      label: "Kick Creator Dashboard, Channel, Stream URL & Key",
      url: "https://dashboard.kick.com",
    },
  },
  x: {
    label: "X (Twitter)",
    ingestUrl: "rtmps://prod-ec-us-west-2.compose.broadcast.live-video.net:443/rtmp/",
    placeholder: "xxxxxxxxxxxx",
    keyHelp: {
      label: "X Media Studio Producer",
      url: "https://help.x.com/en/using-x/how-to-use-live-producer",
    },
  },
  custom: {
    label: "Custom RTMP/SRT",
    ingestUrl: "",
    placeholder: "rtmp://your-server.com/live/stream-key",
  },
};
