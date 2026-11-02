// Contact form topics. Links elsewhere on the site open /contact?topic=<id>
// with the form and hero set for that topic.

export const CONTACT_TOPICS = [
  {
    id: "engineering",
    label: "A project or technical question",
    title: "Talk to the engineers",
    description:
      "You reach the video engineers who build FrameWorks and MistServer. Tell us what you are building, from encoder to player.",
  },
  {
    id: "trial",
    label: "Trial usage",
    title: "Ask for trial usage",
    description:
      "Tell us what you want to test and roughly how much traffic it needs. We set up trial usage on your account by hand.",
  },
  {
    id: "pricing",
    label: "Custom pricing",
    title: "Ask for custom pricing",
    description:
      "For volumes past the published plans, committed capacity, or invoice and procurement terms. Tell us about the workload.",
  },
  {
    id: "channel",
    label: "A shared Slack or WhatsApp channel",
    title: "Ask for a shared channel",
    description:
      "Work with our engineers in a shared Slack or WhatsApp channel. Tell us which one your team uses.",
  },
  {
    id: "other",
    label: "Something else",
    title: "Contact us",
    description:
      "Support, partnerships, press, or bug reports. Choose a channel below or send a note.",
  },
];

export const DEFAULT_CONTACT_TOPIC = "engineering";

// Returns the topic for a ?topic= value, falling back to the default for a
// missing or unknown value.
export function contactTopic(value) {
  return (
    CONTACT_TOPICS.find((topic) => topic.id === value) ??
    CONTACT_TOPICS.find((topic) => topic.id === DEFAULT_CONTACT_TOPIC)
  );
}

export const contactPath = (topic) => `/contact?topic=${encodeURIComponent(topic)}`;
